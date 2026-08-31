// alertfix lifecycle persistence (v0.30).
//
// 動機:
//
//	v0.27 で alert_fix を実装した時点では state を持たず、毎回 evaluate して
//	全 alert を出していた。これでは:
//	  1. 同じ alert が永遠に発火 — agent loop が無限ループ的に同じ提案を受け取る
//	  2. resolve / snooze 不可能 — 「あとで対応」ができない
//	  3. cortex flywheel ④ Alert-Fix の閉ループに到達しない
//
//	本 file は JSONL 永続化 + 3 action (resolve / snooze / reopen) を追加。
//
// 設計判断:
//   - audit.log と同じく O_APPEND な JSONL — 1 entry が atomic、corrupt-line
//     tolerance、race-free な複数 process write OK (POSIX < PIPE_BUF)
//   - 最新 entry が "current state" — replay-friendly、過去履歴を捨てない
//   - in-memory map に load + 各 update 時に append + map 更新
//   - empty / missing file は zero state — first run でも安全
//
// ADR-0001 ゼロ依存:
//   - encoding/json (stdlib)
//   - os / time (stdlib)
//   - sync (stdlib)
package alertfix

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// LifecycleStatus は alert の現在の状態。
type LifecycleStatus string

const (
	// StatusActive は発火中で未対応の alert 状態。
	StatusActive LifecycleStatus = "active"
	// StatusResolved は対応完了として解決済みの alert 状態。
	StatusResolved LifecycleStatus = "resolved"
	// StatusSnoozed はスヌーズ期間中で一時的に通知抑制された alert 状態。
	StatusSnoozed LifecycleStatus = "snoozed"
)

// StateEntry は JSONL に書かれる 1 行。
//
// Action は "resolve" / "snooze" / "reopen" のいずれか。replay 用に過去履歴を
// 全部残す。
type StateEntry struct {
	AlertID     string          `json:"alert_id"`
	Action      string          `json:"action"`
	Status      LifecycleStatus `json:"status"`
	Note        string          `json:"note,omitempty"`
	SnoozeUntil *time.Time      `json:"snooze_until,omitempty"`
	Timestamp   time.Time       `json:"timestamp"`
}

// CurrentState は 1 alert_id の現状(replay 後の最新)。
type CurrentState struct {
	AlertID     string          `json:"alert_id"`
	Status      LifecycleStatus `json:"status"`
	Note        string          `json:"note,omitempty"`
	SnoozeUntil *time.Time      `json:"snooze_until,omitempty"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// Store は lifecycle state の永続化 + memory cache。
//
// daemon プロセスで singleton を共有(server.go で 1 つ作って alert_fix /
// alert_resolve handler に inject)。
type Store struct {
	path string
	mu   sync.RWMutex
	curr map[string]*CurrentState

	NowFn func() time.Time // test hook
}

// NewStore は path に永続化される Store を作る。
//
// path 親 directory が存在しない場合は 0755 で作る。file が存在しなければ
// 空 state で start。file 存在する場合は全 entry を replay して memory map を
// 構築する。corrupt line は skip(error log は呼出側で扱う)。
func NewStore(path string) (*Store, error) {
	s := &Store{
		path:  path,
		curr:  map[string]*CurrentState{},
		NowFn: time.Now,
	}
	if path == "" {
		return s, nil // in-memory only mode
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("alertfix: mkdir %s: %w", dir, err)
		}
	}
	if err := s.replay(); err != nil {
		return nil, err
	}
	return s, nil
}

// replay は file を読み memory map に展開する。
//
// 最新 entry の状態を採用。snooze の期限切れ判定は Get / FilterAlerts /
// Stats で都度行う(NowFn が NewStore 後に上書きされる test を考慮、
// replay 時刻は使わない)。
func (s *Store) replay() error {
	f, err := os.Open(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("alertfix: open %s: %w", s.path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// 巨大行対応: 1 MB まで
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e StateEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue // corrupt-line tolerance
		}
		if e.AlertID == "" {
			continue
		}
		s.curr[e.AlertID] = &CurrentState{
			AlertID:     e.AlertID,
			Status:      e.Status,
			Note:        e.Note,
			SnoozeUntil: e.SnoozeUntil,
			UpdatedAt:   e.Timestamp,
		}
	}
	return sc.Err()
}

// Get は alert_id の現状を返す(なければ nil + false)。
//
// snooze 期限切れは active 扱いで返す。
func (s *Store) Get(alertID string) (*CurrentState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	st, ok := s.curr[alertID]
	if !ok {
		return nil, false
	}
	if st.Status == StatusSnoozed && st.SnoozeUntil != nil && s.NowFn().After(*st.SnoozeUntil) {
		// snapshot を copy、active 化
		c := *st
		c.Status = StatusActive
		return &c, true
	}
	c := *st
	return &c, true
}

// Resolve は alert を resolved にして JSONL append する。
//
// note は自由文(対応内容を記録)。
func (s *Store) Resolve(alertID, note string) error {
	e := StateEntry{
		AlertID:   alertID,
		Action:    "resolve",
		Status:    StatusResolved,
		Note:      note,
		Timestamp: s.NowFn(),
	}
	return s.apply(e)
}

// Snooze は alert を期限付きで一時抑制する。
//
// until が past なら error。
func (s *Store) Snooze(alertID string, until time.Time, note string) error {
	if !until.After(s.NowFn()) {
		return errors.New("alertfix: snooze until must be in the future")
	}
	e := StateEntry{
		AlertID:     alertID,
		Action:      "snooze",
		Status:      StatusSnoozed,
		Note:        note,
		SnoozeUntil: &until,
		Timestamp:   s.NowFn(),
	}
	return s.apply(e)
}

// Reopen は resolved/snoozed を active に戻す。
func (s *Store) Reopen(alertID, note string) error {
	e := StateEntry{
		AlertID:   alertID,
		Action:    "reopen",
		Status:    StatusActive,
		Note:      note,
		Timestamp: s.NowFn(),
	}
	return s.apply(e)
}

// apply は entry を append + memory map 更新。
func (s *Store) apply(e StateEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path != "" {
		f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return fmt.Errorf("alertfix: open %s: %w", s.path, err)
		}
		defer f.Close()
		raw, err := json.Marshal(e)
		if err != nil {
			return err
		}
		if _, err := f.Write(append(raw, '\n')); err != nil {
			return err
		}
	}
	s.curr[e.AlertID] = &CurrentState{
		AlertID:     e.AlertID,
		Status:      e.Status,
		Note:        e.Note,
		SnoozeUntil: e.SnoozeUntil,
		UpdatedAt:   e.Timestamp,
	}
	return nil
}

// FilterAlerts は alert 配列から resolved / snoozed を除外する(active のみ残す)。
//
// 用途: alert_fix の評価結果から、ユーザーが「あとで対応」とした alert を消す。
func (s *Store) FilterAlerts(alerts []Alert) []Alert {
	s.mu.RLock()
	defer s.mu.RUnlock()
	now := s.NowFn()
	// **空でも `[]`**(v1.3.3)。フィルタは Report を作り直す側なので、
	// コンストラクタだけ直しても null は復活する——不変条件は
	// **全ての生産点** で保たれなければ意味がない(実測で live tool が null を返した)。
	out := []Alert{}
	for _, a := range alerts {
		st, ok := s.curr[a.ID]
		if !ok {
			out = append(out, a)
			continue
		}
		// resolved → skip
		if st.Status == StatusResolved {
			continue
		}
		// snoozed → 期限内なら skip
		if st.Status == StatusSnoozed && st.SnoozeUntil != nil && now.Before(*st.SnoozeUntil) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// FilterReport は resolved / snoozed を除外し、Report の集計
// (Total / BySeverity / BySource / ByProject / HasCritical)を再計算して返す。
// yagura_alert_fix と daemon の health sweep の双方が使う(lifecycle filter の single
// source of truth)。s が nil の場合は呼出側が判断する(ここでは前提として non-nil)。
func (s *Store) FilterReport(r Report) Report {
	r.Alerts = s.FilterAlerts(r.Alerts)
	r.Total = len(r.Alerts)
	r.BySeverity = map[Severity]int{}
	r.BySource = map[Source]int{}
	r.ByProject = map[string]int{}
	r.HasCritical = false
	for _, a := range r.Alerts {
		r.BySeverity[a.Severity]++
		r.BySource[a.Source]++
		r.ByProject[a.Project]++
		if a.Severity == SevCritical {
			r.HasCritical = true
		}
	}
	return r
}

// Snapshot は現状の全 state を return する(test / debug 用)。
func (s *Store) Snapshot() []CurrentState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]CurrentState, 0, len(s.curr))
	for _, st := range s.curr {
		out = append(out, *st)
	}
	return out
}

// Stats は active / resolved / snoozed の現状件数を返す。
func (s *Store) Stats() map[LifecycleStatus]int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	stats := map[LifecycleStatus]int{
		StatusActive:   0,
		StatusResolved: 0,
		StatusSnoozed:  0,
	}
	now := s.NowFn()
	for _, st := range s.curr {
		// snooze 期限切れは active として count
		eff := st.Status
		if eff == StatusSnoozed && st.SnoozeUntil != nil && now.After(*st.SnoozeUntil) {
			eff = StatusActive
		}
		stats[eff]++
	}
	return stats
}
