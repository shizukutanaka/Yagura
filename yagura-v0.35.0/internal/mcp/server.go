// server.go: JSON-RPC 2.0 over HTTP server + tool registration.
package mcp

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shizukutanaka/yagura/internal/alertfix"
	"github.com/shizukutanaka/yagura/internal/audit"
	"github.com/shizukutanaka/yagura/internal/dedupe"
	"github.com/shizukutanaka/yagura/internal/hookreceiver"
)

const (
	// 1 request body の上限(JSON-RPC payload はせいぜい数 KB)
	maxBodyBytes = 1 * 1024 * 1024
	// 1 tool 実行の timeout
	defaultToolTimeout = 55 * time.Second
)

// HandlerCtx は MCP tool ハンドラのシグネチャ。
// args は tool が期待する形に json.Unmarshal 済みの状態で来る(generic で受ける形にしないのは zero-dep 維持のため)。
type HandlerCtx func(ctx context.Context, args json.RawMessage) (result any, err error)

// Tool は MCP に公開する単一の tool 定義。
type Tool struct {
	Name        string     // "yagura_list" 等
	Description string     // Claude 等の LLM が tool 選択時に読む説明
	InputSchema any        // JSON Schema (object literal 想定)
	Handler     HandlerCtx // 実装
}

// Server は MCP HTTP server。Mihari と同様のパターン:
//   - ServeHTTP で /mcp endpoint を受け、JSON-RPC を捌く
//   - tool は Register() で動的に追加可能
//   - 認証は Bearer Token (token == "" なら無認証 → local 限定)
//   - 全エラーは asJSONError で正規化されて JSON-RPC error にマップ
//   - panic は recover してログ出力、500 を返す
type Server struct {
	token  string
	logger *slog.Logger

	mu    sync.RWMutex
	tools map[string]*Tool

	// audit sink. nil 許容(設定されなければ書込まない)。
	// SetAudit() で起動時に注入する。
	audit audit.Sink

	// v0.17.0: per-tool token usage stats(計測のみ、副作用なし)。
	// atomic.AddUint64 で増分するため lock 不要。read は statsMu で snapshot を取る。
	statsMu sync.RWMutex
	stats   map[string]*ToolStats

	// v0.23.0: deduplication cache (content-addressed). quality_check / secretscan /
	// sbom 等の deterministic な scan 結果を hash で再利用。
	// nil 許容(無効にしたい場合)。
	cache *dedupe.Cache

	// v0.30.0: alert lifecycle store. resolved / snoozed の永続化 + filter。
	// nil 許容(無効化したい場合 or test)。
	alertStore *alertfix.Store

	// v0.31.0: Claude Code HTTP hooks receiver
	hookReceiver *hookreceiver.Receiver
}

// ToolStats は単一 tool の累積使用量を表す。
//
// 動機:
//
//	v0.16 で description / JSON を圧縮したが、効果を end-to-end で測定する手段が無かった。
//	各 tool の request/response byte 数を累積記録し、yagura_token_stats で取得可能にする。
//
// 計測単位は byte ベース(token ではない)。LLM の tokenizer は client 側で異なるため、
// yagura は generic な byte 数のみ report する。byte/token 比は概ね 3-4x なので
// 削減効果のオーダーは byte 比でも捉えられる。
type ToolStats struct {
	Name          string    `json:"name"`
	Calls         uint64    `json:"calls"`
	RequestBytes  uint64    `json:"request_bytes"`  // 累積入力 byte
	ResponseBytes uint64    `json:"response_bytes"` // 累積出力 byte
	ErrorCount    uint64    `json:"error_count"`
	LastCallAt    time.Time `json:"last_call_at,omitempty"`
}

// AvgReqBytes は平均 request byte 数。Calls=0 で 0 を返す。
func (s *ToolStats) AvgReqBytes() float64 {
	if s.Calls == 0 {
		return 0
	}
	return float64(s.RequestBytes) / float64(s.Calls)
}

// AvgRespBytes は平均 response byte 数。
func (s *ToolStats) AvgRespBytes() float64 {
	if s.Calls == 0 {
		return 0
	}
	return float64(s.ResponseBytes) / float64(s.Calls)
}

// New は Server を生成する。token == "" は無認証(127.0.0.1 bind 前提)。
func New(token string, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		token:  token,
		logger: logger,
		tools:  make(map[string]*Tool),
		stats:  make(map[string]*ToolStats),
		cache:  dedupe.New(0, 0), // 0 / 0 で default (256 entries, 1 hour TTL)
	}
}

// Cache は dedupe cache の reference を返す(tool builder から参照する用)。
func (s *Server) Cache() *dedupe.Cache {
	return s.cache
}

// CacheStats は dedupe cache の累積統計を返す。
func (s *Server) CacheStats() dedupe.Stats {
	if s.cache == nil {
		return dedupe.Stats{}
	}
	return s.cache.Stats()
}

// recordStats は tool 呼出 1 回分の計測を記録する(原子的)。
func (s *Server) recordStats(name string, reqBytes, respBytes int, errored bool, now time.Time) {
	s.statsMu.Lock()
	st, ok := s.stats[name]
	if !ok {
		st = &ToolStats{Name: name}
		s.stats[name] = st
	}
	atomic.AddUint64(&st.Calls, 1)
	atomic.AddUint64(&st.RequestBytes, uint64(reqBytes))
	atomic.AddUint64(&st.ResponseBytes, uint64(respBytes))
	if errored {
		atomic.AddUint64(&st.ErrorCount, 1)
	}
	st.LastCallAt = now
	s.statsMu.Unlock()
}

// AllToolStats は全 tool の累積 stats を snapshot として返す。
func (s *Server) AllToolStats() []ToolStats {
	s.statsMu.RLock()
	defer s.statsMu.RUnlock()
	out := make([]ToolStats, 0, len(s.stats))
	for _, st := range s.stats {
		// atomic load で snapshot を取る
		out = append(out, ToolStats{
			Name:          st.Name,
			Calls:         atomic.LoadUint64(&st.Calls),
			RequestBytes:  atomic.LoadUint64(&st.RequestBytes),
			ResponseBytes: atomic.LoadUint64(&st.ResponseBytes),
			ErrorCount:    atomic.LoadUint64(&st.ErrorCount),
			LastCallAt:    st.LastCallAt,
		})
	}
	return out
}

// Register は tool を追加する。同名既存があれば上書き。
func (s *Server) Register(t *Tool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tools[t.Name] = t
}

// SetAudit は audit sink を後付けで注入する。
// nil を渡すと audit 書込を停止する。
func (s *Server) SetAudit(sink audit.Sink) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.audit = sink
}

// SetAlertStore は alert lifecycle store を設定する(v0.30.0)。
//
// nil を渡すと filtering / resolve / snooze 機能は無効化される(従来 v0.27 動作)。
func (s *Server) SetAlertStore(st *alertfix.Store) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.alertStore = st
}

// AlertStore は現在の store を返す(nil 可)。
func (s *Server) AlertStore() *alertfix.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.alertStore
}

// emitAudit は audit sink がセットされていれば 1 record を書く。
// 失敗は logger に出すのみで処理は続行する(audit failure で MCP 機能を止めない)。
func (s *Server) emitAudit(r audit.Record) {
	s.mu.RLock()
	sink := s.audit
	s.mu.RUnlock()
	if sink == nil {
		return
	}
	if err := sink.Append(r); err != nil {
		s.logger.Warn("mcp: audit append failed", "err", err, "kind", r.Kind)
	}
}

// ToolNames は登録済み tool 名を返す(ヘルスチェック等の表示用)。
func (s *Server) ToolNames() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	names := make([]string, 0, len(s.tools))
	for n := range s.tools {
		names = append(names, n)
	}
	sort.Strings(names) // deterministic order (Deterministic output rule)
	return names
}

// ServeHTTP は POST /mcp を受ける。Method 違いは 405、body 違いは JSON-RPC error。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Auth check (constant-time comparison で timing attack 対策).
	// 空 token は無認証扱い(loopback bind 前提、ADR-0004)。
	if s.token != "" {
		got := r.Header.Get("Authorization")
		const prefix = "Bearer "
		// 1. プレフィックス長確認(分岐は OK、長さは secret ではない)
		// 2. 全長を揃えてから constant-time 比較
		// 3. 一致しない場合は同じ "unauthorized" を返す(error message からの情報漏洩防止)
		ok := false
		if strings.HasPrefix(got, prefix) {
			received := got[len(prefix):]
			// 長さが違う場合も constant-time にするため、必ず ConstantTimeCompare まで通す。
			// ConstantTimeCompare は長さが違うと 0 を返す。
			if subtle.ConstantTimeCompare([]byte(received), []byte(s.token)) == 1 {
				ok = true
			}
		}
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxBodyBytes))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var req struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  string          `json:"method"`
		Params  struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeJSONRPCError(w, nil, -32700, "parse error", nil)
		return
	}

	switch req.Method {
	case "initialize":
		// Mihari と同じ最小 initialize 応答
		writeJSONRPCResult(w, req.ID, map[string]any{
			"protocolVersion": "2024-11-05",
			"serverInfo": map[string]any{
				"name":    "yagura",
				"version": "0.1.0",
			},
			"capabilities": map[string]any{"tools": map[string]any{}},
		})
	case "tools/list":
		s.handleToolsList(w, req.ID)
	case "tools/call":
		s.handleToolsCall(r.Context(), w, req.ID, req.Params.Name, req.Params.Arguments)
	default:
		writeJSONRPCError(w, req.ID, -32601, "method not found", nil)
	}
}

// handleToolsList returns the tools/list response, honoring opt-in compact mode.
//
// Compact mode (env YAGURA_MCP_COMPACT=1):
//
//	v0.22.0 で追加。Anthropic の Tool Search Tool / defer_loading が API-side 機能で
//	server から advertise できないため、server-internal の opt-in compact mode を提供。
//	- description を最小化 ([G]/[S] prefix のみ等)
//	- InputSchema は type + required のみに簡略化(properties detail を削除)
//	- tool 名から意味を推測する前提
//	client は yagura_tools_catalog を補完手段として呼べる。
func (s *Server) handleToolsList(w http.ResponseWriter, id json.RawMessage) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	compact := os.Getenv("YAGURA_MCP_COMPACT") == "1"
	// Emit in name-sorted order: tools/list is the cacheable prefix the client
	// sends every turn, so a stable order keeps its prompt/KV cache warm
	// (a randomized Go-map order would silently bust it). Also satisfies the
	// Deterministic output rule.
	ordered := make([]*Tool, 0, len(s.tools))
	for _, t := range s.tools {
		ordered = append(ordered, t)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	list := make([]map[string]any, 0, len(ordered))
	for _, t := range ordered {
		entry := map[string]any{"name": t.Name}
		if compact {
			entry["description"] = compactDescription(t.Description)
			if s, ok := t.InputSchema.(map[string]any); ok {
				entry["inputSchema"] = compactSchema(s)
			} else {
				entry["inputSchema"] = t.InputSchema
			}
		} else {
			entry["description"] = t.Description
			entry["inputSchema"] = t.InputSchema
		}
		list = append(list, entry)
	}
	writeJSONRPCResult(w, id, map[string]any{"tools": list})
}

// compactDescription extracts only the [G]/[S] prefix from a description.
// 47 byte 平均 → 4 byte 平均 になる(93% 削減)。
func compactDescription(desc string) string {
	if len(desc) >= 3 {
		switch desc[:3] {
		case "[G]", "[S]":
			return desc[:3]
		}
	}
	return ""
}

// compactSchema strips per-property metadata, keeping only type and required.
// 174 byte 平均 → ~50 byte 平均 になる(~70% 削減)。
func compactSchema(schema map[string]any) map[string]any {
	out := map[string]any{"type": "object"}
	if req, ok := schema["required"]; ok {
		out["required"] = req
	}
	// properties は key 一覧のみ(値は type=string で固定、LLM は tool 名+catalog で推測)
	if props, ok := schema["properties"].(map[string]any); ok && len(props) > 0 {
		minProps := map[string]any{}
		for k := range props {
			minProps[k] = map[string]any{"type": "string"}
		}
		out["properties"] = minProps
	}
	return out
}

func (s *Server) handleToolsCall(ctx context.Context, w http.ResponseWriter,
	id json.RawMessage, name string, args json.RawMessage) {

	startedAt := time.Now()

	s.mu.RLock()
	tool, ok := s.tools[name]
	s.mu.RUnlock()
	if !ok {
		s.emitAudit(audit.Record{
			Kind:   "mcp_call_unknown_tool",
			Actor:  "mcp",
			Target: name,
		})
		writeJSONRPCError(w, id, -32601, "unknown tool: "+name, nil)
		return
	}

	// per-call timeout + panic recovery
	cctx, cancel := context.WithTimeout(ctx, defaultToolTimeout)
	defer cancel()

	var (
		result  any
		callErr error
	)
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.logger.Error("mcp: tool panic",
					"tool", name,
					"panic", fmt.Sprint(r),
					"stack", string(debug.Stack()))
				callErr = &ToolError{
					Code:    "internal",
					Message: "internal error",
					Cause:   fmt.Errorf("panic: %v", r),
				}
			}
		}()
		result, callErr = tool.Handler(cctx, args)
	}()

	if callErr != nil {
		s.logger.Warn("mcp: tool returned error", "tool", name, "err", callErr)
		s.emitAudit(audit.Record{
			Kind:   "mcp_call_failed",
			Actor:  "mcp",
			Target: name,
			Fields: map[string]any{
				"duration_ms": time.Since(startedAt).Milliseconds(),
				"error":       callErr.Error(),
			},
		})
		errJSON := asJSONError(callErr)
		// v0.17.0: stats 計測(error response も byte 数記録)
		s.recordStats(name, len(args), len(errJSON), true, time.Now())
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"jsonrpc":"2.0","id":`))
		idBytes := id
		if len(idBytes) == 0 {
			idBytes = []byte("null")
		}
		w.Write(idBytes)
		w.Write([]byte(`,"error":`))
		w.Write(errJSON)
		w.Write([]byte(`}`))
		return
	}

	s.emitAudit(audit.Record{
		Kind:   "mcp_call_ok",
		Actor:  "mcp",
		Target: name,
		Fields: map[string]any{
			"duration_ms": time.Since(startedAt).Milliseconds(),
		},
	})

	// MCP convention: tool result is wrapped in `content` array.
	resultJSON, err := json.Marshal(result)
	if err != nil {
		writeJSONRPCError(w, id, -32603, "serialize error", err)
		return
	}
	// v0.17.0: stats 計測。content wrapper も含めた JSON 全体のサイズで記録
	// (LLM が受け取る実体に近い計測)。
	s.recordStats(name, len(args), len(resultJSON), false, time.Now())
	writeJSONRPCResult(w, id, map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": string(resultJSON)},
		},
	})
}

func writeJSONRPCResult(w http.ResponseWriter, id json.RawMessage, result any) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(coalesceID(id)),
		"result":  result,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func writeJSONRPCError(w http.ResponseWriter, id json.RawMessage, code int, msg string, _ error) {
	resp := map[string]any{
		"jsonrpc": "2.0",
		"id":      json.RawMessage(coalesceID(id)),
		"error": map[string]any{
			"code":    code,
			"message": msg,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func coalesceID(id json.RawMessage) []byte {
	if len(id) == 0 {
		return []byte("null")
	}
	return id
}

// Suppress unused-package warning when this file is the only mcp consumer in tests.
var _ = errors.New

// SetHookReceiver は Claude Code hook receiver を設定する(v0.31.0)。
//
// nil 許容(無効化したい場合 or test)。
func (s *Server) SetHookReceiver(hr *hookreceiver.Receiver) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hookReceiver = hr
}

// HookReceiver は現在の receiver を返す (nil 可)。
func (s *Server) HookReceiver() *hookreceiver.Receiver {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hookReceiver
}
