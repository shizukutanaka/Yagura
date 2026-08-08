// Package quotamonitor tracks per-agent usage state for automatic handoff
// between Claude Code and Windsurf (v0.13.0).
//
// 動機:
//
//	m は Claude Code を main で使う(Pro/Max plan)。usage policy は
//	5-hour rolling window + weekly cap で、使い切ると新 prompt が即 block される。
//	v0.13.0 では Windsurf を fallback として運用できるよう、両 agent の
//	quota state を yagura が hub として管理する。
//
// 仕組み:
//   - 各 agent (claude_code / windsurf) は独立した State machine を持つ
//   - State: ACTIVE (≥ WarnThreshold) → WARN → EXHAUSTED → SWITCHED
//   - quota 値は agent 側が能動 report する(MCP yagura_quota_report 経由)。
//     外部 API では subscription tier の残量取得が不可能なため。
//   - Recommend() で next agent を返す(EXHAUSTED 検出時に切替先候補)
//
// 設計判断:
//   - ゼロ依存(ADR-0001)
//   - thread-safe: 内部 mutex で並列 report 可能
//   - 永続化はオプショナル(SaveFn / LoadFn hook)
package quotamonitor

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Agent は監視対象の AI コーディング agent 識別子。
type Agent string

const (
	// AgentClaudeCode は Claude Code エージェント。
	AgentClaudeCode Agent = "claude_code"
	// AgentWindsurf は Windsurf エージェント。
	AgentWindsurf Agent = "windsurf"
)

// State は agent の現在状態。
type State string

const (
	// StateActive は残量が WarnThreshold 以上の通常状態。
	StateActive State = "ACTIVE"
	// StateWarn は残量が WarnThreshold 未満かつ 0 超の警告状態。
	StateWarn State = "WARN"
	// StateExhausted は残量 0(または 429 受信)で枯渇した状態。
	StateExhausted State = "EXHAUSTED"
	// StateSwitched は他 agent に handoff 済みの suspend 状態。
	StateSwitched State = "SWITCHED"
)

// AgentStatus は単一 agent の状態スナップショット。
//
// v0.22.0 omitempty 補強:
//
//	起動直後の zero time が "0001-01-01T00:00:00Z"(30 byte) として出力されるのを抑止。
//	field 自体が missing なら "未報告" を意味する。
type AgentStatus struct {
	Agent            Agent     `json:"agent"`
	State            State     `json:"state"`
	RemainingPercent int       `json:"remaining_percent"` // 0-100, always populated
	WindowResetsAt   time.Time `json:"window_resets_at,omitempty"`
	WeeklyResetsAt   time.Time `json:"weekly_resets_at,omitempty"`
	LastReportAt     time.Time `json:"last_report_at,omitempty"` // v0.22.0: zero time omit
	LastReportSource string    `json:"last_report_source,omitempty"`
	HandoffAt        time.Time `json:"handoff_at,omitempty"`
	LastHeartbeatAt  time.Time `json:"last_heartbeat_at,omitempty"`
}

// Monitor は両 agent の状態を保持する hub。
type Monitor struct {
	// WarnThreshold(%) を下回ると State = WARN(デフォルト 20)
	WarnThreshold int
	// NowFn はテスト用時刻 hook(nil なら time.Now)
	NowFn func() time.Time

	mu        sync.RWMutex
	statuses  map[Agent]*AgentStatus
	histories map[Agent][]ReportEvent // v0.15.0: forecast 用 ring buffer

	// v0.17.0: optional history persistence
	persistMu   sync.RWMutex
	persistPath string
}

// New は標準 Monitor を生成する。
//
// 初期状態: 両 agent とも ACTIVE / 100%。
func New() *Monitor {
	m := &Monitor{
		WarnThreshold: 20,
		statuses:      map[Agent]*AgentStatus{},
		histories:     map[Agent][]ReportEvent{},
	}
	// 初期 status を埋める
	for _, a := range []Agent{AgentClaudeCode, AgentWindsurf} {
		m.statuses[a] = &AgentStatus{
			Agent:            a,
			State:            StateActive,
			RemainingPercent: 100,
			LastReportAt:     m.now(),
		}
		m.histories[a] = nil
	}
	return m
}

// Report は agent から quota 残量を受け取って状態を更新する。
//
// remainingPercent: 0-100。0 なら EXHAUSTED、< WarnThreshold なら WARN。
// source: "manual" (m が手動報告) / "auto" (Claude Code /usage parse) / "429" (rate limit hit)
// windowReset / weeklyReset は optional(zero time なら無視)
func (m *Monitor) Report(agent Agent, remainingPercent int, source string,
	windowReset, weeklyReset time.Time) error {

	if !validAgent(agent) {
		return fmt.Errorf("unknown agent: %s", agent)
	}
	if remainingPercent < 0 || remainingPercent > 100 {
		return fmt.Errorf("remainingPercent out of range: %d (must be 0..100)", remainingPercent)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	st := m.statuses[agent]
	st.RemainingPercent = remainingPercent
	st.LastReportAt = now
	st.LastReportSource = source
	if !windowReset.IsZero() {
		st.WindowResetsAt = windowReset
	}
	if !weeklyReset.IsZero() {
		st.WeeklyResetsAt = weeklyReset
	}

	// v0.15.0: history を ring buffer 形式で保持(forecast 用)
	newEvent := ReportEvent{
		At:               now,
		RemainingPercent: remainingPercent,
		Source:           source,
	}
	h := append(m.histories[agent], newEvent)
	if len(h) > ForecastWindowSize {
		// 最古を捨てる
		h = h[len(h)-ForecastWindowSize:]
	}
	m.histories[agent] = h

	// state 計算(SWITCHED は手動 unswitched まで保持)
	if st.State == StateSwitched {
		// v0.17.0: persistence(SWITCHED でも history は記録する)
		go m.persistReport(agent, newEvent)
		return nil
	}
	switch {
	case source == "429" || remainingPercent == 0:
		st.State = StateExhausted
	case remainingPercent < m.warnThreshold():
		st.State = StateWarn
	default:
		st.State = StateActive
	}
	// v0.17.0: persistence(別 goroutine で fire-and-forget、Report は即返却)
	go m.persistReport(agent, newEvent)
	return nil
}

// MarkSwitched は agent を SWITCHED 状態にマークする(handoff 完了時)。
func (m *Monitor) MarkSwitched(agent Agent) error {
	if !validAgent(agent) {
		return fmt.Errorf("unknown agent: %s", agent)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.statuses[agent]
	st.State = StateSwitched
	st.HandoffAt = m.now()
	return nil
}

// MarkResumed は agent を SWITCHED から ACTIVE/WARN に戻す(復帰時)。
// remainingPercent が指定されていればその値を基に状態判定。
func (m *Monitor) MarkResumed(agent Agent, remainingPercent int) error {
	if !validAgent(agent) {
		return fmt.Errorf("unknown agent: %s", agent)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	st := m.statuses[agent]
	st.RemainingPercent = remainingPercent
	st.LastReportAt = m.now()
	if remainingPercent == 0 {
		st.State = StateExhausted
	} else if remainingPercent < m.warnThreshold() {
		st.State = StateWarn
	} else {
		st.State = StateActive
	}
	return nil
}

// Status は指定 agent の現在状態を返す(コピー)。
func (m *Monitor) Status(agent Agent) (AgentStatus, error) {
	if !validAgent(agent) {
		return AgentStatus{}, fmt.Errorf("unknown agent: %s", agent)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	return *m.statuses[agent], nil
}

// AllStatuses は両 agent の状態をスナップショットで返す。
func (m *Monitor) AllStatuses() map[Agent]AgentStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[Agent]AgentStatus, len(m.statuses))
	for k, v := range m.statuses {
		out[k] = *v
	}
	return out
}

// Recommend は現在使うべき agent を返す。
//
// 判定ロジック (v0.15.0 で stale 統合):
//   - 両方とも (EXHAUSTED or SWITCHED or stale) → "" (両方使えない、待つしかない)
//   - 片方が unusable → 他方
//   - 両方 usable → 残量が多い方
//
// usable 判定: State が ACTIVE/WARN かつ heartbeat が stale でない。
// 一度も heartbeat なしの状態は stale 扱いしない(起動直後の grace period)。
//
// 戻り値の reason は人間向け説明。
func (m *Monitor) Recommend() (Agent, string) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	cc := m.statuses[AgentClaudeCode]
	ws := m.statuses[AgentWindsurf]

	ccUsable, ccReason := m.usabilityLocked(cc)
	wsUsable, wsReason := m.usabilityLocked(ws)

	switch {
	case !ccUsable && !wsUsable:
		nextReset := earliestReset(cc, ws)
		return "", fmt.Sprintf(
			"both agents unavailable (claude_code: %s; windsurf: %s). Earliest reset: %s",
			ccReason, wsReason, nextReset)
	case !ccUsable:
		return AgentWindsurf, fmt.Sprintf(
			"claude_code unavailable (%s); fall back to windsurf",
			ccReason)
	case !wsUsable:
		return AgentClaudeCode, fmt.Sprintf(
			"windsurf unavailable (%s); use claude_code",
			wsReason)
	case cc.RemainingPercent >= ws.RemainingPercent:
		return AgentClaudeCode, fmt.Sprintf(
			"claude_code has more remaining (%d%% vs windsurf %d%%)",
			cc.RemainingPercent, ws.RemainingPercent)
	default:
		return AgentWindsurf, fmt.Sprintf(
			"windsurf has more remaining (%d%% vs claude_code %d%%)",
			ws.RemainingPercent, cc.RemainingPercent)
	}
}

// usabilityLocked は agent が今使える状態かを判定する(mu 取得済み前提)。
//
// unusable と判定する条件:
//  1. State が EXHAUSTED or SWITCHED
//  2. heartbeat が IdleTimeout 超え(ただし一度も heartbeat なしは除外)
//
// usable な場合の reason は短い state 説明、unusable は理由を含む。
func (m *Monitor) usabilityLocked(s *AgentStatus) (usable bool, reason string) {
	if s.State == StateExhausted {
		return false, fmt.Sprintf("EXHAUSTED (remaining %d%%)", s.RemainingPercent)
	}
	if s.State == StateSwitched {
		return false, "SWITCHED (handed off, awaiting resume)"
	}
	// heartbeat 判定: 一度も無い状態は grace period(起動直後)とみなす
	if !s.LastHeartbeatAt.IsZero() {
		elapsed := m.now().Sub(s.LastHeartbeatAt)
		if elapsed > DefaultIdleTimeout {
			return false, fmt.Sprintf("stale (no heartbeat for %v)",
				elapsed.Round(time.Second))
		}
	}
	return true, fmt.Sprintf("%s (%d%%)", s.State, s.RemainingPercent)
}

// ShouldHandoff は現在 agent から別 agent への切替を提案すべきか判定する。
//
// 切替推奨条件:
//   - current が EXHAUSTED → 即切替(true, target)
//   - current が WARN かつ other が ACTIVE → 切替推奨(true, target)
//   - それ以外 → false
//
// 戻り値:
//
//	should: 切替推奨か
//	target: 切替先 agent("" なら切替先なし)
//	reason: 人間向け説明
func (m *Monitor) ShouldHandoff(current Agent) (bool, Agent, string) {
	if !validAgent(current) {
		return false, "", "unknown agent: " + string(current)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	cur := m.statuses[current]
	var other *AgentStatus
	var otherAgent Agent
	if current == AgentClaudeCode {
		other = m.statuses[AgentWindsurf]
		otherAgent = AgentWindsurf
	} else {
		other = m.statuses[AgentClaudeCode]
		otherAgent = AgentClaudeCode
	}
	otherUsable := other.State == StateActive || other.State == StateWarn

	switch cur.State {
	case StateExhausted:
		if otherUsable {
			return true, otherAgent, fmt.Sprintf(
				"%s exhausted; %s is %s with %d%% remaining",
				current, otherAgent, other.State, other.RemainingPercent)
		}
		return false, "", fmt.Sprintf(
			"%s exhausted but %s also unavailable (%s)", current, otherAgent, other.State)
	case StateWarn:
		if other.State == StateActive && other.RemainingPercent > cur.RemainingPercent {
			return true, otherAgent, fmt.Sprintf(
				"%s in WARN (%d%%); %s is ACTIVE with %d%% — handoff recommended",
				current, cur.RemainingPercent, otherAgent, other.RemainingPercent)
		}
	}
	return false, "", fmt.Sprintf("%s is %s with %d%% remaining; no handoff needed",
		current, cur.State, cur.RemainingPercent)
}

// ─── helpers ─────────────────────────────────────────────────

func (m *Monitor) now() time.Time {
	if m.NowFn != nil {
		return m.NowFn()
	}
	return time.Now()
}

func (m *Monitor) warnThreshold() int {
	if m.WarnThreshold <= 0 {
		return 20
	}
	return m.WarnThreshold
}

func validAgent(a Agent) bool {
	return a == AgentClaudeCode || a == AgentWindsurf
}

func earliestReset(a, b *AgentStatus) string {
	pickEarliest := func(t1, t2 time.Time) time.Time {
		if t1.IsZero() {
			return t2
		}
		if t2.IsZero() {
			return t1
		}
		if t1.Before(t2) {
			return t1
		}
		return t2
	}
	t := pickEarliest(a.WindowResetsAt, b.WindowResetsAt)
	t = pickEarliest(t, a.WeeklyResetsAt)
	t = pickEarliest(t, b.WeeklyResetsAt)
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format(time.RFC3339)
}

// AgentFromString は柔軟な input(case-insensitive、aliases)から Agent を解決する。
//
// 入力 → Agent:
//
//	"claude_code", "claude-code", "claude", "cc" → AgentClaudeCode
//	"windsurf", "cascade", "ws" → AgentWindsurf
func AgentFromString(s string) (Agent, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "claude_code", "claude-code", "claude", "cc":
		return AgentClaudeCode, nil
	case "windsurf", "cascade", "ws":
		return AgentWindsurf, nil
	}
	return "", fmt.Errorf("unknown agent name: %q (expected claude_code or windsurf)", s)
}

// ════════════════════════════════════════════════════════════════════════
// v0.14.0: Heartbeat protocol
// ────────────────────────────────────────────────────────────────────────
//
// 動機:
//   v0.13.0 の state machine は "quota 残量低下" は検知できるが、
//   "agent プロセス自体が応答していない" は検知できない。たとえば:
//     - Claude Code が remaining=80% でも、ユーザが画面を閉じている
//     - Windsurf がクラッシュしたが yagura は気づかない
//   この場合 Recommend() は失われた agent を推奨し続けてしまう。
//
// 解決:
//   各 agent が定期的に `RecordHeartbeat()` を呼ぶ(MCP tool yagura_heartbeat 経由)。
//   IdleTimeout(デフォルト 10 分)経過で silent な agent は STALE 候補にする。
//
// 設計判断:
//   - STALE は state ではなく derived property (LastHeartbeatAt + Now の比較)
//   - 既存 4 状態(ACTIVE/WARN/EXHAUSTED/SWITCHED)に直交する
//   - Recommend() は STALE な agent を候補外に
//   - IdleTimeout は調整可能(test では数秒、本番は分単位)
// ════════════════════════════════════════════════════════════════════════

// IdleTimeout default for STALE detection.
const DefaultIdleTimeout = 10 * time.Minute

// RecordHeartbeat は agent からの "alive" 通知を受け取る。
//
// 引数:
//
//	agent: AgentClaudeCode or AgentWindsurf
//
// 副作用:
//
//	LastHeartbeatAt = m.now()
func (m *Monitor) RecordHeartbeat(agent Agent) error {
	if !validAgent(agent) {
		return fmt.Errorf("unknown agent: %s", agent)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.statuses[agent].LastHeartbeatAt = m.now()
	return nil
}

// IsStale は agent が IdleTimeout 以上 silent か判定する。
//
// 戻り値:
//
//	stale: true なら timeout 超過
//	sinceLastHeartbeat: 最終 heartbeat からの経過時間
func (m *Monitor) IsStale(agent Agent, idleTimeout time.Duration) (stale bool, sinceLastHeartbeat time.Duration) {
	if !validAgent(agent) {
		return false, 0
	}
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	st := m.statuses[agent]
	if st.LastHeartbeatAt.IsZero() {
		// 一度も heartbeat なし → SWITCHED 以外なら stale 扱い(controlled silence は OK)
		if st.State == StateSwitched {
			return false, 0
		}
		return true, 0
	}
	elapsed := m.now().Sub(st.LastHeartbeatAt)
	return elapsed > idleTimeout, elapsed
}

// AnyStale は両 agent のうち stale な agent を列挙する。
//
// runtime visibility 用(dashboard で表示)。
func (m *Monitor) AnyStale(idleTimeout time.Duration) []Agent {
	var stale []Agent
	for _, a := range []Agent{AgentClaudeCode, AgentWindsurf} {
		if s, _ := m.IsStale(a, idleTimeout); s {
			stale = append(stale, a)
		}
	}
	return stale
}

// ════════════════════════════════════════════════════════════════════════
// v0.15.0: Background watchdog
// ────────────────────────────────────────────────────────────────────────
//
// 動機:
//   v0.14.0 の IsStale は on-demand(Recommend / Dashboard 表示時)に
//   評価される。stale になった瞬間をログに残さないので、後から
//   「いつ Claude Code が落ちた?」を調べるのが困難だった。
//
// 解決:
//   daemon 起動時に Watch goroutine を起動。一定間隔で各 agent の
//   IsStale をチェックし、新しく stale になった/復帰した agent を log。
//
// 設計判断:
//   - 状態変化のみログ(spam 防止: 1 分毎の単純ポーリングではない)
//   - context.Done で停止
//   - emit 関数を inject(log/slog だけでなく metric 出力も可能に)
// ════════════════════════════════════════════════════════════════════════

// StaleEvent は agent の stale 状態変化を表す。
type StaleEvent struct {
	Agent       Agent
	BecameStale bool // true なら active→stale、false なら復帰
	At          time.Time
	Elapsed     time.Duration // BecameStale=true 時の last heartbeat からの経過
}

// Watch は IdleTimeout 経過した agent の状態変化を監視する。
//
// 引数:
//
//	ctx: context.Done で goroutine 停止
//	interval: 評価間隔(0 なら 30 秒)
//	idleTimeout: stale 判定閾値(0 なら DefaultIdleTimeout)
//	emit: 状態変化時に呼ばれる callback
//
// この関数は呼出側のゴルーチンで動く(self-block)。go func() でラップする
// 想定。状態変化のみ emit するので、emit は log/metric の両方で使用可能。
func (m *Monitor) Watch(ctx context.Context, interval, idleTimeout time.Duration,
	emit func(StaleEvent)) {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if idleTimeout <= 0 {
		idleTimeout = DefaultIdleTimeout
	}
	if emit == nil {
		emit = func(StaleEvent) {} // no-op
	}

	// 前回 stale 状態(change-detection 用)
	prevStale := map[Agent]bool{}
	for _, a := range []Agent{AgentClaudeCode, AgentWindsurf} {
		s, _ := m.IsStale(a, idleTimeout)
		prevStale[a] = s
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, a := range []Agent{AgentClaudeCode, AgentWindsurf} {
				now, elapsed := m.IsStale(a, idleTimeout)
				if now != prevStale[a] {
					emit(StaleEvent{
						Agent:       a,
						BecameStale: now,
						At:          m.now(),
						Elapsed:     elapsed,
					})
					prevStale[a] = now
				}
			}
		}
	}
}

// ════════════════════════════════════════════════════════════════════════
// v0.16.0: Usage summary
// ────────────────────────────────────────────────────────────────────────
//
// 動機:
//   ユーザーは「Claude Code / Windsurf それぞれの使用量を確認したい」と
//   要望。現在の状態 (state, remaining_percent) は v0.13 から表示されて
//   いるが、"今まで何回 report したか" "直近 1h で何 % 減ったか" 等の
//   累積指標は見えなかった。
//
// 提供する指標:
//   TotalReports        — 累計 report 回数
//   Consumed1h / 24h    — 直近期間の総消費量(remaining% の落ち幅)
//   AvgConsumePerHour   — history 全体での平均消費速度
//   SlopePercentPerSec  — 現在の消費速度(Forecast の slope)
//   LastConsumeAt       — 最後に remaining が減少した時刻
//   Samples             — sparkline 描画用の生データ(history のコピー)
//
// 実装メモ:
//   - 過去データは Monitor.histories の ring buffer (最大 10 件) のみ
//     daemon 再起動で失われる(persist は v0.17 候補)
//   - 短期データ専用なので "7 日累計" 等は提供しない(誤解防止)
// ════════════════════════════════════════════════════════════════════════

// MinReliableWindowMinutes は AvgConsumePerHour を出す最小 window。
//
// 動機(v0.17.0):
//
//	v0.16 の test smoke で 0.3 秒間隔の Report 投入時に
//	`avg_consume_per_hour: 250857` のような非現実値が出力された。
//	分母の windowHours が極小だと per-hour 換算が爆発する数学的問題。
//
// 解決:
//
//	window が 5 分未満なら avg/h を 0 で返す(omitempty で消える)。
//	呼出側は Reliable flag で短期データかどうか区別できる。
const MinReliableWindowMinutes = 5

// UsageSummary は単一 agent の使用量サマリ。
type UsageSummary struct {
	Agent              Agent         `json:"agent"`
	TotalReports       int           `json:"total_reports"`
	WindowHours        float64       `json:"window_hours"`          // history が cover する時間幅
	Consumed1h         float64       `json:"consumed_1h,omitempty"` // 直近 1h での remaining 減少量
	Consumed24h        float64       `json:"consumed_24h,omitempty"`
	AvgConsumePerHour  float64       `json:"avg_consume_per_hour,omitempty"`  // history 全体での平均速度 (window 不足時 0)
	SlopePercentPerSec float64       `json:"slope_percent_per_sec,omitempty"` // 現在の消費速度
	LastConsumeAt      time.Time     `json:"last_consume_at,omitempty"`
	CurrentPercent     int           `json:"current_percent"`
	Samples            []ReportEvent `json:"samples,omitempty"` // sparkline data(直近 10 件、古→新)
	// v0.17.0: 統計の信頼性 flag。WindowHours < MinReliableWindowMinutes/60 で false。
	// false なら AvgConsumePerHour / SlopePercentPerSec を信頼してはいけない。
	Reliable bool `json:"reliable"`
}

// UsageSummary は agent の使用量集計を返す。
func (m *Monitor) UsageSummary(agent Agent) UsageSummary {
	if !validAgent(agent) {
		return UsageSummary{Agent: agent}
	}
	m.mu.RLock()
	history := append([]ReportEvent(nil), m.histories[agent]...) // copy under lock
	currentPercent := m.statuses[agent].RemainingPercent
	m.mu.RUnlock()

	summary := UsageSummary{
		Agent:          agent,
		TotalReports:   len(history),
		CurrentPercent: currentPercent,
		Samples:        history,
	}
	if len(history) == 0 {
		return summary
	}

	now := m.now()
	first := history[0]
	last := history[len(history)-1]
	windowDuration := last.At.Sub(first.At)
	summary.WindowHours = windowDuration.Hours()
	// v0.17.0: 信頼性判定 — window が 5 分未満なら per-hour 系を出さない
	minWindow := time.Duration(MinReliableWindowMinutes) * time.Minute
	summary.Reliable = windowDuration >= minWindow

	// Consumed1h / 24h: 期間内最古 remaining → 現在 remaining の差
	// これは絶対量なので window 短くても意味がある(直近 1h で 50% 落ちた、等)
	summary.Consumed1h = consumedSince(history, now.Add(-1*time.Hour), currentPercent)
	summary.Consumed24h = consumedSince(history, now.Add(-24*time.Hour), currentPercent)

	// AvgConsumePerHour / SlopePercentPerSec は per-time 値なので
	// window が極小だと数学的に爆発する。信頼できる場合のみ計算。
	if summary.Reliable && windowDuration > 0 {
		drop := float64(first.RemainingPercent - last.RemainingPercent)
		summary.AvgConsumePerHour = drop / windowDuration.Hours()
		summary.SlopePercentPerSec = float64(last.RemainingPercent-first.RemainingPercent) / windowDuration.Seconds()
	}

	// LastConsumeAt: history を逆走して最後に remaining が下がった瞬間
	for i := len(history) - 1; i > 0; i-- {
		if history[i].RemainingPercent < history[i-1].RemainingPercent {
			summary.LastConsumeAt = history[i].At
			break
		}
	}

	return summary
}

// consumedSince は cutoff 以降の最古サンプル remaining - 現在 remaining を返す。
// cutoff より前のサンプルしかなければ 0(期間内データ不足扱い)。
func consumedSince(history []ReportEvent, cutoff time.Time, currentPercent int) float64 {
	for _, ev := range history {
		if !ev.At.Before(cutoff) {
			return float64(ev.RemainingPercent - currentPercent)
		}
	}
	return 0
}

// AllUsageSummaries は両 agent のサマリを返す。
func (m *Monitor) AllUsageSummaries() map[Agent]UsageSummary {
	out := map[Agent]UsageSummary{}
	for _, a := range []Agent{AgentClaudeCode, AgentWindsurf} {
		out[a] = m.UsageSummary(a)
	}
	return out
}
