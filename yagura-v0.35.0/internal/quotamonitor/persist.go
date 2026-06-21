// persist.go: per-agent history persistence (v0.17.0)
//
// 動機:
//
//	v0.16 までは Monitor.histories は完全 in-memory で、daemon 再起動で
//	sparkline data と forecast 履歴が全失。実運用で yagura を毎日再起動
//	する m のワークフローではほぼ常にデータ空 → forecast/usage_summary が
//	無意味になる。
//
// 解決:
//
//	各 Report 直後に append-only JSONL に 1 行追記。
//	起動時に LoadHistory(path) で全 line を replay し、agent 毎に直近
//	ForecastWindowSize 件をメモリ復元。
//
// 設計判断:
//   - JSONL (1 line = 1 ReportEvent + agent name)
//     → crash safe: 部分書込みでも前の line までは無効化されない
//   - 起動時 read は時間順保持(各 line append-only 前提)
//   - 失敗時(disk full / 権限 error)は silent、daemon は継続
package quotamonitor

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"
)

// persistEntry は JSONL 1 line の double-format(legacy + compact)対応 struct。
//
// v0.17.0 から legacy format で書込み開始したが、v0.22.0 で compact format を追加。
//   - 旧 format: {"agent":"claude_code","at":"2026-05-13T07:42:25.168817789Z","remaining_percent":100,"source":"auto"} (95+ byte/line)
//   - 新 format: {"a":"cc","t":1715620945,"r":100,"s":"auto"} (~40 byte/line, 58% 削減)
//
// 書込みは compact のみ。読込みは両方を受け入れる(backward-compat)。
// 既存 usage_history.jsonl にあった legacy line も問題なく読み込める。
type persistEntry struct {
	// compact format (v0.22.0+ 書込み形式)
	A string `json:"a,omitempty"` // agent (cc=claude_code / ws=windsurf)
	T int64  `json:"t,omitempty"` // unix timestamp (seconds)
	R int    `json:"r,omitempty"` // remaining_percent
	S string `json:"s,omitempty"` // source (auto/manual/429)

	// legacy format (v0.17 〜 v0.21 で書込まれた旧 line を読込むため)
	Agent            Agent  `json:"agent,omitempty"`
	At               string `json:"at,omitempty"` // RFC3339Nano
	RemainingPercent int    `json:"remaining_percent,omitempty"`
	Source           string `json:"source,omitempty"`
}

// compactAgent は Agent enum を 2-char compact form に変換する。
func compactAgent(a Agent) string {
	switch a {
	case AgentClaudeCode:
		return "cc"
	case AgentWindsurf:
		return "ws"
	default:
		return string(a)
	}
}

// expandAgent は 2-char compact form を Agent enum に戻す。
// 旧 format の "claude_code" / "windsurf" 全文も受け付ける。
func expandAgent(s string) Agent {
	switch s {
	case "cc":
		return AgentClaudeCode
	case "ws":
		return AgentWindsurf
	case string(AgentClaudeCode):
		return AgentClaudeCode
	case string(AgentWindsurf):
		return AgentWindsurf
	default:
		return Agent(s)
	}
}

// resolve は entry の compact / legacy field を統合した Agent / At / R / S を返す。
//
// compact field 優先(新 format)、空なら legacy field を使う。
// これで両 format が同じ struct で扱える。
func (e *persistEntry) resolve() (Agent, time.Time, int, string) {
	var agent Agent
	if e.A != "" {
		agent = expandAgent(e.A)
	} else if e.Agent != "" {
		agent = e.Agent
	}
	var at time.Time
	if e.T > 0 {
		at = time.Unix(e.T, 0).UTC()
	} else if e.At != "" {
		if t, err := time.Parse(time.RFC3339Nano, e.At); err == nil {
			at = t
		}
	}
	remaining := e.R
	if remaining == 0 && e.RemainingPercent != 0 {
		// compact の R=0 と legacy の RemainingPercent=0 を区別できないので、
		// legacy 側に値があればそれを優先(0 は両 format で valid な意味を持つ)
		remaining = e.RemainingPercent
	}
	source := e.S
	if source == "" {
		source = e.Source
	}
	return agent, at, remaining, source
}

// SetPersistPath は永続化 file path を設定する。
// 空文字なら永続化無効(default)。
func (m *Monitor) SetPersistPath(path string) {
	m.persistMu.Lock()
	defer m.persistMu.Unlock()
	m.persistPath = path
}

// LoadHistory は path から JSONL を読み込み、agent 毎の history を rebuild する。
// file が存在しない場合は no-op、エラーなし。不正 line は silent skip。
func (m *Monitor) LoadHistory(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	temp := map[Agent][]ReportEvent{}
	br := bufio.NewReader(f)
	for {
		line, err := br.ReadBytes('\n')
		if len(line) > 0 {
			var e persistEntry
			if perr := json.Unmarshal(line, &e); perr == nil {
				agent, at, remaining, source := e.resolve()
				if validAgent(agent) {
					ev := ReportEvent{
						At:               at,
						RemainingPercent: remaining,
						Source:           source,
					}
					temp[agent] = append(temp[agent], ev)
				}
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
	}

	// 直近 ForecastWindowSize 件のみ keep + 時系列順 sort
	// v0.17.0: 最新サンプルから AgentStatus も復元(再起動で current_percent が
	// default 100 に戻ってしまう問題を解決)
	m.mu.Lock()
	for a, evs := range temp {
		sort.Slice(evs, func(i, j int) bool { return evs[i].At.Before(evs[j].At) })
		if len(evs) > ForecastWindowSize {
			evs = evs[len(evs)-ForecastWindowSize:]
		}
		m.histories[a] = evs
		// AgentStatus 復元
		if len(evs) > 0 {
			last := evs[len(evs)-1]
			st := m.statuses[a]
			st.RemainingPercent = last.RemainingPercent
			st.LastReportAt = last.At
			st.LastReportSource = last.Source
			// state を再計算(SWITCHED は永続化されないので考慮不要)
			switch {
			case last.Source == "429" || last.RemainingPercent == 0:
				st.State = StateExhausted
			case last.RemainingPercent < m.warnThreshold():
				st.State = StateWarn
			default:
				st.State = StateActive
			}
		}
	}
	m.mu.Unlock()
	return nil
}

// persistReport は Report 後に 1 行を JSONL に追記する。
// 失敗してもエラーを伝播しない(fire-and-forget)。
func (m *Monitor) persistReport(agent Agent, ev ReportEvent) {
	m.persistMu.RLock()
	path := m.persistPath
	m.persistMu.RUnlock()
	if path == "" {
		return
	}
	// v0.22.0: compact form で書込み(58% 削減)
	// legacy field (Agent, At, RemainingPercent, Source) は空のまま
	entry := persistEntry{
		A: compactAgent(agent),
		T: ev.At.UTC().Unix(), // 秒精度(nano は forecast にも不要)
		R: ev.RemainingPercent,
		S: ev.Source,
	}
	data, err := json.Marshal(entry)
	if err != nil {
		return
	}
	data = append(data, '\n')
	// O_APPEND で原子的追記(POSIX 仕様; 単一 line << PIPE_BUF なので concurrent safe)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(data)
}
