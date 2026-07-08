// Package hookreceiver は Claude Code の HTTP hooks を受信する。
//
// Motivation (v0.31.0):
//
//	Claude Code は 2026-02 に HTTP hooks (`type: "http"`) を GA。
//	PreToolUse / PostToolUse / Stop / SubagentStop 等のイベントを
//	localhost:8080/hooks/* に POST する spec が確立した。が、それを受け取って
//	活用する deterministic local backend がエコシステムに不足している
//	(GitHub anthropics/claude-code#4995 で要望、未実装)。
//
//	yagura は既に local HTTP server + Bearer token auth + JSONL persist +
//	registry (cwd → project mapping) を持つので、最も自然な host。
//
// 設計判断:
//   - 観察モード (v0.31): allow/deny は返さない、純粋に記録のみ。response は
//     空 JSON `{}` で execution を続行させる。Claude Code は non-2xx を
//     non-blocking error として扱うので、yagura 障害が agent を止めない。
//   - JSONL persist (audit.log と同じ pattern): {state_dir}/claude_hooks.jsonl
//   - cwd → project mapping: registry の LocalPath で prefix match。マッチ
//     しなければ "unknown" として記録(後で project register することで遡及
//     検索可能)。
//   - In-memory aggregator: session_id → []Event、project → counters。
//     起動時に JSONL を replay して構築。
//   - Trust base: Claude Code 自身が POST してくる → 偽造リスクは local
//     network 限定。Bearer token で簡易認証。
//
// ADR-0001 ゼロ依存: stdlib のみ。
package hookreceiver

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/shizukutanaka/yagura/internal/agentevent"
)

// maxHookBodyBytes は Handle が受け付ける POST body の上限(v0.105.0)。
// /mcp(internal/mcp/server.go: maxBodyBytes = 1 MiB)と同じ桁——hook event は
// tool_input/tool_response を含んでも通常小さい JSON なので同水準で十分。
const maxHookBodyBytes = 1 * 1024 * 1024

// Event は Claude Code が送ってくる hook event の正規化形式。
//
// 公式 schema (https://code.claude.com/docs/en/hooks) の field を抜き出し、
// 共通項を Event に、固有 fields を Extra (raw json.RawMessage) に格納する。
type Event struct {
	// 共通 fields
	HookEventName string    `json:"hook_event_name"`
	SessionID     string    `json:"session_id,omitempty"`
	CWD           string    `json:"cwd,omitempty"`
	Timestamp     time.Time `json:"timestamp"`

	// 解決後
	Project string `json:"project,omitempty"` // cwd → registry lookup 結果

	// tool 関連 (PreToolUse / PostToolUse / PostToolUseFailure)
	ToolName     string          `json:"tool_name,omitempty"`
	ToolUseID    string          `json:"tool_use_id,omitempty"`
	ToolInput    json.RawMessage `json:"tool_input,omitempty"`
	ToolResponse json.RawMessage `json:"tool_response,omitempty"`
	DurationMS   int64           `json:"duration_ms,omitempty"` // PostToolUse only

	// subagent 関連
	AgentID   string `json:"agent_id,omitempty"`
	AgentType string `json:"agent_type,omitempty"`

	// Stop / SubagentStop
	LastAssistantMessage string `json:"last_assistant_message,omitempty"`

	// 失敗 flag
	IsError bool `json:"is_error,omitempty"` // PostToolUseFailure 等
}

// ProjectLookup は cwd から project slug を解決する interface。
//
// registry.Registry を直接 import せず injection 経由(循環 import 回避)。
type ProjectLookup interface {
	ResolveByPath(cwd string) (slug string, ok bool)
}

// Stats は project 別の集計値。
type Stats struct {
	Total      int            `json:"total"`
	ByEvent    map[string]int `json:"by_event"`
	ByTool     map[string]int `json:"by_tool"`
	ErrorCount int            `json:"error_count"`
	TotalMS    int64          `json:"total_ms"`
	LastEvent  time.Time      `json:"last_event"`
}

// clone は Stats の deep copy を返す(map を複製する)。
//
// ProjectStats / AllStats が返す値は、呼出側が lock なしで range する。一方 receiver は
// 受信のたびに同じ map を mutate する。shallow copy(`*st`)だと map header が live map を
// 指したままなので、「concurrent map iteration and map write」で fatal panic し得る。
// 必ず clone してから返すことで、呼出側は私有 map を安全に走査できる。
func (s Stats) clone() Stats {
	byEvent := make(map[string]int, len(s.ByEvent))
	for k, v := range s.ByEvent {
		byEvent[k] = v
	}
	byTool := make(map[string]int, len(s.ByTool))
	for k, v := range s.ByTool {
		byTool[k] = v
	}
	s.ByEvent = byEvent
	s.ByTool = byTool
	return s
}

// Receiver は HTTP hook events を受け取って persist + 集計する。
//
// daemon プロセス singleton (server.go 経由で inject)。
type Receiver struct {
	path   string
	lookup ProjectLookup

	mu     sync.RWMutex
	stats  map[string]*Stats // project slug → Stats
	recent []Event           // ring buffer (most recent N)
	maxBuf int

	NowFn func() time.Time
}

// NewReceiver は path に JSONL persist し、起動時に既存 entry を replay する。
//
// path 空: in-memory only (test 用)。
// maxBuf: in-memory に保持する最新 event 数 (replay は ring に最後 maxBuf 件)。
func NewReceiver(path string, lookup ProjectLookup, maxBuf int) (*Receiver, error) {
	if maxBuf <= 0 {
		maxBuf = 10000
	}
	r := &Receiver{
		path:   path,
		lookup: lookup,
		stats:  map[string]*Stats{},
		recent: make([]Event, 0, maxBuf),
		maxBuf: maxBuf,
		NowFn:  time.Now,
	}
	if path == "" {
		return r, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("hookreceiver: mkdir %s: %w", dir, err)
		}
	}
	if err := r.replay(); err != nil {
		return nil, err
	}
	return r, nil
}

// replay は既存 JSONL を読み込んで in-memory aggregator を warm-up する。
//
// corrupt-line tolerance: 1 行壊れても他は読める (audit.log と同じ defensive)。
func (r *Receiver) replay() error {
	f, err := os.Open(r.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("hookreceiver: open %s: %w", r.path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024) // 1 MB lines OK
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		r.applyToMemory(e)
	}
	return sc.Err()
}

// applyToMemory は event を ring buffer と stats に反映する (lock 取得済前提)。
func (r *Receiver) applyToMemoryLocked(e Event) {
	// ring buffer
	if len(r.recent) >= r.maxBuf {
		// shift 1 (FIFO eviction)
		r.recent = r.recent[1:]
	}
	r.recent = append(r.recent, e)

	// stats by project
	proj := e.Project
	if proj == "" {
		proj = "unknown"
	}
	st, ok := r.stats[proj]
	if !ok {
		st = &Stats{
			ByEvent: map[string]int{},
			ByTool:  map[string]int{},
		}
		r.stats[proj] = st
	}
	st.Total++
	st.ByEvent[e.HookEventName]++
	if e.ToolName != "" {
		st.ByTool[e.ToolName]++
	}
	if e.IsError {
		st.ErrorCount++
	}
	st.TotalMS += e.DurationMS
	if e.Timestamp.After(st.LastEvent) {
		st.LastEvent = e.Timestamp
	}
}

// applyToMemory は public で lock 取得。
func (r *Receiver) applyToMemory(e Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.applyToMemoryLocked(e)
}

// Handle は POST /hooks/claude-code エンドポイントの本体。
//
// 1. body を JSON parse
// 2. cwd → project resolve (lookup 経由)
// 3. JSONL append + memory 反映
// 4. response: {} (observation mode)
func (r *Receiver) Handle(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// body を上限で切る(/mcp・HTTP API と同様、外部到達可能な endpoint での
	// 無制限読込は memory-exhaustion DoS の温床)。
	req.Body = http.MaxBytesReader(w, req.Body, maxHookBodyBytes)
	// raw payload を保存しておく(parsing 失敗時も生 JSON を JSONL に残す)
	var raw map[string]json.RawMessage
	dec := json.NewDecoder(req.Body)
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}

	e := r.parseEvent(raw)
	e.Timestamp = r.NowFn()

	// cwd → project resolve
	if e.CWD != "" && r.lookup != nil {
		if slug, ok := r.lookup.ResolveByPath(e.CWD); ok {
			e.Project = slug
		}
	}

	// JSONL persist (best effort、失敗で agent を止めない)
	if r.path != "" {
		if err := r.append(e); err != nil {
			// log only、response は 200 で agent 継続
			// 実際 production では Logger 経由だが、ここでは silent (caller がエラー処理)
			_ = err
		}
	}
	r.applyToMemory(e)

	// observation mode: 空 response (== Claude Code に decision を残す)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{}`))
}

// parseEvent は raw JSON から正規化 Event を抽出する。
//
// Claude Code の各 hook event は schema が違うので、共通項のみを Event に
// pack する。
func (r *Receiver) parseEvent(raw map[string]json.RawMessage) Event {
	e := Event{}
	unmarshalString := func(key string, dst *string) {
		if v, ok := raw[key]; ok {
			json.Unmarshal(v, dst)
		}
	}
	unmarshalString("hook_event_name", &e.HookEventName)
	unmarshalString("session_id", &e.SessionID)
	unmarshalString("cwd", &e.CWD)
	unmarshalString("tool_name", &e.ToolName)
	unmarshalString("tool_use_id", &e.ToolUseID)
	unmarshalString("agent_id", &e.AgentID)
	unmarshalString("agent_type", &e.AgentType)
	unmarshalString("last_assistant_message", &e.LastAssistantMessage)
	if v, ok := raw["tool_input"]; ok {
		e.ToolInput = v
	}
	if v, ok := raw["tool_response"]; ok {
		e.ToolResponse = v
	}
	if v, ok := raw["duration_ms"]; ok {
		json.Unmarshal(v, &e.DurationMS)
	}

	// 非 Claude Code 形式(hook_event_name 無し)= Gemini CLI / Codex / OTel / 汎用
	// エージェントのイベントは agentevent で正規化し、Claude Code 相当のフィールドへ写す。
	// これで /hooks 取り込みがエージェント非依存になり、hook_stats / timeline がどの
	// エージェントでも効く。既存の Claude Code payload(hook_event_name あり)は不変。
	if e.HookEventName == "" {
		generic := make(map[string]any, len(raw))
		for k, v := range raw {
			var a any
			if json.Unmarshal(v, &a) == nil {
				generic[k] = a
			}
		}
		norm := agentevent.Normalize(generic)
		e.HookEventName = hookNameFor(norm.Operation, norm.Phase)
		if e.ToolName == "" {
			e.ToolName = norm.Tool
		}
		if e.SessionID == "" {
			e.SessionID = norm.Session
		}
		if e.AgentType == "" {
			e.AgentType = norm.Agent
		}
		if e.DurationMS == 0 {
			e.DurationMS = norm.DurationMs
		}
		if norm.Phase == agentevent.PhaseError {
			e.IsError = true
		}
	}

	// error 判定: PostToolUseFailure or tool_response.is_error true
	if e.HookEventName == "PostToolUseFailure" {
		e.IsError = true
	} else if len(e.ToolResponse) > 0 {
		var tr struct {
			IsError bool `json:"is_error"`
		}
		json.Unmarshal(e.ToolResponse, &tr)
		e.IsError = tr.IsError
	}
	return e
}

// hookNameFor は agentevent の (operation, phase) を Claude Code 相当の
// hook event 名へ写す。hook_stats の語彙を全エージェントで揃えるため。
func hookNameFor(op, phase string) string {
	switch op {
	case agentevent.OpExecuteTool:
		switch phase {
		case agentevent.PhaseStart:
			return "PreToolUse"
		case agentevent.PhaseError:
			return "PostToolUseFailure"
		default:
			return "PostToolUse"
		}
	case agentevent.OpInvokeAgent:
		if phase == agentevent.PhaseStart {
			return "SessionStart"
		}
		return "Stop"
	case agentevent.OpChat:
		return "Notification"
	}
	if op == "" {
		return "Notification"
	}
	return op + ":" + phase
}

// append は JSONL に 1 entry 追記する (atomic、O_APPEND)。
func (r *Receiver) append(e Event) error {
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	raw, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = f.Write(append(raw, '\n'))
	return err
}

// Timeline は時間範囲 + filter で events を返す (新しい順)。
func (r *Receiver) Timeline(slug string, since time.Time, eventType string, limit int) []Event {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if limit <= 0 {
		limit = 100
	}
	var out []Event
	// recent は古い順なので逆順 traverse
	for i := len(r.recent) - 1; i >= 0; i-- {
		e := r.recent[i]
		if !e.Timestamp.After(since) {
			break
		}
		if slug != "" && e.Project != slug {
			continue
		}
		if eventType != "" && e.HookEventName != eventType {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// AllStats は全 project の集計を sort 安定で返す。
func (r *Receiver) AllStats() map[string]Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]Stats, len(r.stats))
	for k, v := range r.stats {
		out[k] = v.clone() // deep copy: caller ranges these maps without the lock
	}
	return out
}

// ProjectStats は単一 project の stats を返す (なければ zero)。
func (r *Receiver) ProjectStats(slug string) Stats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if st, ok := r.stats[slug]; ok {
		return st.clone() // deep copy: caller ranges these maps without the lock
	}
	return Stats{ByEvent: map[string]int{}, ByTool: map[string]int{}}
}

// TopTools は最も使われた tool TOP-N (集計、新しい順 tie-break)。
func (r *Receiver) TopTools(slug string, n int) []ToolUsage {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if n <= 0 {
		n = 10
	}
	var st *Stats
	if slug == "" {
		// 全 project 集計
		agg := map[string]int{}
		for _, s := range r.stats {
			for t, c := range s.ByTool {
				agg[t] += c
			}
		}
		var out []ToolUsage
		for t, c := range agg {
			out = append(out, ToolUsage{Tool: t, Count: c})
		}
		sort.Slice(out, func(i, j int) bool {
			if out[i].Count != out[j].Count {
				return out[i].Count > out[j].Count
			}
			return out[i].Tool < out[j].Tool
		})
		if len(out) > n {
			out = out[:n]
		}
		return out
	}
	st, ok := r.stats[slug]
	if !ok {
		return nil
	}
	var out []ToolUsage
	for t, c := range st.ByTool {
		out = append(out, ToolUsage{Tool: t, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tool < out[j].Tool
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// ToolUsage は集計用 entry。
type ToolUsage struct {
	Tool  string `json:"tool"`
	Count int    `json:"count"`
}
