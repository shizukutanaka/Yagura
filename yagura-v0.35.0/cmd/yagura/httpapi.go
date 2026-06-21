// httpapi.go: CI 統合用 HTTP endpoint(v0.11.0)。
//
// 既存の MCP server (/mcp) は Claude Code 用 JSON-RPC エンドポイントだが、
// CI/CD パイプライン(curl / GitHub Actions / Jenkins)から直接呼ぶには
// JSON-RPC 構文が冗長すぎる。CI 統合のため以下を提供:
//
//   GET  /sbom                 — CycloneDX 1.5 BOM(yagura 自身)
//   POST /gha-audit            — workflow YAML を受けて 7 ルール audit
//   POST /pin-drift            — workflow YAML を受けて pin drift 検出
//
// 認証は既存 MCPToken(Authorization: Bearer ...)を流用。token 未設定なら
// no-auth(localhost 用途想定)。
package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/shizukutanaka/yagura/internal/ghaaudit"
	"github.com/shizukutanaka/yagura/internal/httplimit"
	"github.com/shizukutanaka/yagura/internal/pindrift"
	"github.com/shizukutanaka/yagura/internal/sbom"
)

// httpAPIDeps は HTTP API handler が必要とする依存。
type httpAPIDeps struct {
	Sbom           *sbom.Generator
	Ghaaudit       *ghaaudit.Auditor
	PinDrift       *pindrift.Checker
	MainModulePath string
	MainVersion    string
	AuthToken      string // 空なら認証なし

	// v0.12.0: per-route rate limiters
	// nil なら無制限。各 endpoint で独立した bucket を持つ。
	SbomLimiter     *httplimit.Limiter
	GhaAuditLimiter *httplimit.Limiter
	PinDriftLimiter *httplimit.Limiter
}

// httpRequestBody は POST 系 endpoint 共通の入力。
type httpRequestBody struct {
	Files       map[string]string `json:"files"`
	SummaryOnly bool              `json:"summary_only"`
	Concurrency int               `json:"concurrency"`
}

// registerHTTPAPI は HTTP CI endpoints を mux に登録する。
//
// middleware の重ね順は外側→内側: rate-limit → auth → handler。
// rate-limit を先に通すことで、認証失敗 request も rate-limit 対象にする
// (brute-force token guess 防止)。
func registerHTTPAPI(mux *http.ServeMux, d httpAPIDeps) {
	mux.HandleFunc("/sbom", chainMiddleware(d.handleSBOM, d.SbomLimiter, d.authMiddleware))
	mux.HandleFunc("/gha-audit", chainMiddleware(d.handleGhaAudit, d.GhaAuditLimiter, d.authMiddleware))
	mux.HandleFunc("/pin-drift", chainMiddleware(d.handlePinDrift, d.PinDriftLimiter, d.authMiddleware))
}

// chainMiddleware は handler に rate-limit と auth を重ねる。
// limiter が nil なら rate-limit 無し。
func chainMiddleware(h http.HandlerFunc, limiter *httplimit.Limiter, authMw func(http.HandlerFunc) http.HandlerFunc) http.HandlerFunc {
	wrapped := authMw(h)
	if limiter != nil {
		wrapped = limiter.Middleware(wrapped)
	}
	return wrapped
}

// authMiddleware は AuthToken が設定されていれば Bearer token を検証する。
func (d *httpAPIDeps) authMiddleware(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if d.AuthToken == "" {
			h(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		const prefix = "Bearer "
		// 長さが違う場合も constant-time にするため、HasPrefix を通った場合は
		// 必ず ConstantTimeCompare まで実行する(/mcp と同じタイミング攻撃対策)。
		received := strings.TrimPrefix(auth, prefix)
		if !strings.HasPrefix(auth, prefix) ||
			subtle.ConstantTimeCompare([]byte(received), []byte(d.AuthToken)) != 1 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

// handleSBOM: GET /sbom?summary_only=1
//
// クエリパラメータ:
//   summary_only=1  — Bom 全体ではなく Summary のみ返却(軽量)
func (d *httpAPIDeps) handleSBOM(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	bom, err := d.Sbom.Generate(d.MainModulePath, d.MainVersion)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if r.URL.Query().Get("summary_only") != "" {
		writeJSON(w, http.StatusOK, bom.Summarize())
		return
	}
	writeJSON(w, http.StatusOK, bom)
}

// handleGhaAudit: POST /gha-audit
//
// Request body:
//   {"files": {"path/x.yml": "<yaml content>"}, "summary_only": false}
//
// Response: {"results": {...}, "summary": {...}}
func (d *httpAPIDeps) handleGhaAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	body, err := readJSON(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Files) == 0 {
		writeJSONError(w, http.StatusBadRequest, "files map must contain at least one file")
		return
	}
	results := d.Ghaaudit.AuditDir(".", body.Files)
	if body.SummaryOnly {
		writeJSON(w, http.StatusOK, ghaaudit.Summarize(results))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"summary": ghaaudit.Summarize(results),
	})
}

// handlePinDrift: POST /pin-drift
//
// Request body:
//   {"files": {"path/x.yml": "..."}, "concurrency": 4, "summary_only": false}
//
// Response: {"results": [...], "summary": {...}}
//
// concurrency=0 → デフォルト 4 並列、負値 → serial。
//
// v0.12.0: ?stream=1 で SSE event-stream を返す。大量 pin で CI が進捗を
// 段階的に表示できる。各 event は `data: <json>\n\n` 形式で、JSON は
// pindrift.ResultEvent (index, total_count, result)。
func (d *httpAPIDeps) handlePinDrift(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	body, err := readJSON(r)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(body.Files) == 0 {
		writeJSONError(w, http.StatusBadRequest, "files map must contain at least one file")
		return
	}
	var pins []pindrift.Pin
	for path, content := range body.Files {
		pins = append(pins, pindrift.ExtractPins(path, content)...)
	}
	if len(pins) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"results": []pindrift.Result{},
			"summary": pindrift.Summary{ByStatus: map[string]int{}},
			"note":    "no SHA-pinned uses: found",
		})
		return
	}
	// CI endpoint なので timeout を明示的に設定
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	conc := body.Concurrency
	if conc == 0 {
		conc = 4
	}

	// v0.12.0: SSE streaming mode
	if r.URL.Query().Get("stream") == "1" && conc >= 0 {
		writeSSEPinDrift(ctx, w, d.PinDrift, pins, conc)
		return
	}

	var results []pindrift.Result
	if conc < 0 {
		results = d.PinDrift.CheckPins(ctx, pins)
	} else {
		results = d.PinDrift.CheckPinsParallel(ctx, pins, conc)
	}
	if body.SummaryOnly {
		writeJSON(w, http.StatusOK, pindrift.Summarize(results))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"results": results,
		"summary": pindrift.Summarize(results),
	})
}

// writeSSEPinDrift は CheckPinsStream の出力を SSE で送信する。
// 各 event:
//   event: result
//   data: {"index":N,"total_count":M,"result":{...}}
//
// 最後に done event を送信する:
//   event: done
//   data: {"summary":{...}}
func writeSSEPinDrift(ctx context.Context, w http.ResponseWriter, checker PinDriftStreamer, pins []pindrift.Pin, concurrency int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // nginx 後ろでも buffering 無効化
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSONError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	ch := checker.CheckPinsStream(ctx, pins, concurrency)
	allResults := make([]pindrift.Result, len(pins))
	for ev := range ch {
		data, _ := json.Marshal(ev)
		// SSE 仕様: "event: name\ndata: <data>\n\n"
		w.Write([]byte("event: result\ndata: "))
		w.Write(data)
		w.Write([]byte("\n\n"))
		flusher.Flush()
		if ev.Index >= 0 && ev.Index < len(allResults) {
			allResults[ev.Index] = ev.Result
		}
	}
	// 完了通知
	summary := pindrift.Summarize(allResults)
	sumData, _ := json.Marshal(map[string]any{"summary": summary})
	w.Write([]byte("event: done\ndata: "))
	w.Write(sumData)
	w.Write([]byte("\n\n"))
	flusher.Flush()
}

// PinDriftStreamer は SSE handler が必要とする最小 interface。
// internal/pindrift.Checker.CheckPinsStream を満たす。
type PinDriftStreamer interface {
	CheckPinsStream(ctx context.Context, pins []pindrift.Pin, concurrency int) <-chan pindrift.ResultEvent
}

// ─── helpers ─────────────────────────────────────────────────

// readJSON は POST request body から共通フォーマットを decode する。
// body サイズは 5MB に制限(workflow YAML 23 ファイル分相当)。
func readJSON(r *http.Request) (*httpRequestBody, error) {
	if r.Header.Get("Content-Type") != "" &&
		!strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		return nil, errors.New("Content-Type must be application/json")
	}
	const maxBody = 5 << 20
	data, err := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if err != nil {
		return nil, err
	}
	var body httpRequestBody
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, err
	}
	return &body, nil
}

// writeJSON は v を JSON で書き出す。
//
// v0.16.0: compact JSON が default(token 削減のため)。
// 旧 v0.15 までは 2-space indent default だったが、応答 byte 数で
// 平均 25-35% 削減できるため、人間可読性は ?pretty=1 query に降格。
func writeJSON(w http.ResponseWriter, status int, v any) {
	writeJSONOpts(w, status, v, false)
}

// writeJSONPretty は意図的に pretty-print が必要な場合(debug/CLI)。
func writeJSONPretty(w http.ResponseWriter, status int, v any) {
	writeJSONOpts(w, status, v, true)
}

func writeJSONOpts(w http.ResponseWriter, status int, v any, pretty bool) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	if pretty {
		enc.SetIndent("", "  ")
	}
	enc.Encode(v)
}

// writeJSONError は標準エラーレスポンスを返す。
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// methodNotAllowed は 405 を返す。
func methodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, POST")
	http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
}
