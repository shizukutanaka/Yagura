package cochange

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/shizukutanaka/yagura/internal/churn"
)

// day は t 日目のコミット時刻(決定論的、Date.now を使わない)。
func day(n int) time.Time {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, n)
}

// commit はテスト用のコミット組み立てヘルパ。
func commit(t *testing.T, n int, subject string, paths ...string) churn.Commit {
	t.Helper()
	files := make([]churn.FileChange, 0, len(paths))
	for _, p := range paths {
		files = append(files, churn.FileChange{Path: p, Added: 1})
	}
	return churn.Commit{Hash: subject, When: day(n), Subject: subject, Files: files}
}

func TestAnalyze_CountsSharedRevisions(t *testing.T) {
	commits := []churn.Commit{
		commit(t, 1, "c1", "a.go", "b.go"),
		commit(t, 2, "c2", "a.go", "b.go"),
		commit(t, 3, "c3", "a.go"),
	}
	opts := DefaultOptions()
	opts.MinRevs, opts.MinSharedRevs, opts.MinDegree = 1, 1, 0
	rep := Analyze(commits, opts)
	if len(rep.Pairs) != 1 {
		t.Fatalf("want 1 pair, got %d: %+v", len(rep.Pairs), rep.Pairs)
	}
	p := rep.Pairs[0]
	if p.A != "a.go" || p.B != "b.go" {
		t.Errorf("pair should be sorted (a.go,b.go), got (%s,%s)", p.A, p.B)
	}
	if p.SharedRevs != 2 {
		t.Errorf("shared revs: want 2, got %d", p.SharedRevs)
	}
	if p.RevsA != 3 || p.RevsB != 2 {
		t.Errorf("revs: want A=3 B=2, got A=%d B=%d", p.RevsA, p.RevsB)
	}
}

// 対称な degree と方向性のある confidence は **別の量** であり、両方報告する。
// code-maat は percentage 1 本(degree + average-revs)を出し、Zimmermann の
// association rule は方向つき confidence を使う——片方だけ出すと読み手が
// 「A を触ったら B も触る確率」を対称値だと誤解する。
func TestAnalyze_DegreeIsSymmetricButConfidenceIsDirectional(t *testing.T) {
	// a.go は 10 回、b.go は 2 回変わり、共変更は 2 回。
	var commits []churn.Commit
	for i := 1; i <= 2; i++ {
		commits = append(commits, commit(t, i, "shared", "a.go", "b.go"))
	}
	for i := 3; i <= 10; i++ {
		commits = append(commits, commit(t, i, "solo", "a.go"))
	}
	opts := DefaultOptions()
	opts.MinRevs, opts.MinSharedRevs, opts.MinDegree = 1, 1, 0
	rep := Analyze(commits, opts)
	if len(rep.Pairs) != 1 {
		t.Fatalf("want 1 pair, got %d", len(rep.Pairs))
	}
	p := rep.Pairs[0]
	// 方向性: b.go を触れば a.go は必ず一緒に変わる(1.0)が、逆は 0.2 しかない。
	if math.Abs(p.ConfidenceAB-0.2) > 1e-9 {
		t.Errorf("P(b|a): want 0.2, got %v", p.ConfidenceAB)
	}
	if math.Abs(p.ConfidenceBA-1.0) > 1e-9 {
		t.Errorf("P(a|b): want 1.0, got %v", p.ConfidenceBA)
	}
	// 対称 degree は shared / average(revs) = 2 / 6
	if math.Abs(p.Degree-(2.0/6.0)) > 1e-9 {
		t.Errorf("degree: want %v, got %v", 2.0/6.0, p.Degree)
	}
	if math.Abs(p.AverageRevs-6.0) > 1e-9 {
		t.Errorf("average revs: want 6, got %v", p.AverageRevs)
	}
	// 非対称であることそのものを固定する(対称値で潰さない)。
	if p.ConfidenceAB == p.ConfidenceBA {
		t.Error("confidence must be directional, not symmetric")
	}
}

// 一括変更(フォーマット・license header・generated code)は N*(N-1)/2 個の
// 偽の結合を一撃で作る。code-maat が --max-changeset-size を既定 30 で持つ理由。
func TestAnalyze_SkipsSweepingChangesets(t *testing.T) {
	sweep := make([]string, 40)
	for i := range sweep {
		sweep[i] = fmt.Sprintf("sweep-%02d.go", i) // 40 個とも別ファイルであること
	}
	commits := []churn.Commit{commit(t, 1, "gofmt everything", sweep...)}
	opts := DefaultOptions()
	opts.MinRevs, opts.MinSharedRevs, opts.MinDegree = 1, 1, 0
	rep := Analyze(commits, opts)
	if len(rep.Pairs) != 0 {
		t.Errorf("a 40-file changeset must not create pairs, got %d", len(rep.Pairs))
	}
	if rep.CommitsSkippedLarge != 1 {
		t.Errorf("skipped-large count: want 1, got %d", rep.CommitsSkippedLarge)
	}
	if rep.CommitsUsed != 0 {
		t.Errorf("commits used: want 0, got %d", rep.CommitsUsed)
	}
}

func TestAnalyze_MinSharedRevsFilters(t *testing.T) {
	commits := []churn.Commit{
		commit(t, 1, "c1", "a.go", "b.go"),
		commit(t, 2, "c2", "a.go", "b.go"),
	}
	opts := DefaultOptions()
	opts.MinRevs, opts.MinDegree = 1, 0
	opts.MinSharedRevs = 3
	if rep := Analyze(commits, opts); len(rep.Pairs) != 0 {
		t.Errorf("min_shared_revs=3 must drop a 2-share pair, got %d pairs", len(rep.Pairs))
	}
	opts.MinSharedRevs = 2
	if rep := Analyze(commits, opts); len(rep.Pairs) != 1 {
		t.Errorf("min_shared_revs=2 must keep a 2-share pair, got %d pairs", len(rep.Pairs))
	}
}

func TestAnalyze_MinDegreeFilters(t *testing.T) {
	// degree = 2 / avg(10,2) = 0.333
	var commits []churn.Commit
	for i := 1; i <= 2; i++ {
		commits = append(commits, commit(t, i, "shared", "a.go", "b.go"))
	}
	for i := 3; i <= 10; i++ {
		commits = append(commits, commit(t, i, "solo", "a.go"))
	}
	opts := DefaultOptions()
	opts.MinRevs, opts.MinSharedRevs = 1, 1
	opts.MinDegree = 0.5
	if rep := Analyze(commits, opts); len(rep.Pairs) != 0 {
		t.Errorf("min_degree=0.5 must drop a 0.33 pair, got %d", len(rep.Pairs))
	}
	opts.MinDegree = 0.3
	if rep := Analyze(commits, opts); len(rep.Pairs) != 1 {
		t.Errorf("min_degree=0.3 must keep a 0.33 pair, got %d", len(rep.Pairs))
	}
}

func TestAnalyze_DeterministicOrder(t *testing.T) {
	commits := []churn.Commit{
		commit(t, 1, "c1", "a.go", "b.go", "c.go"),
		commit(t, 2, "c2", "a.go", "b.go"),
	}
	opts := DefaultOptions()
	opts.MinRevs, opts.MinSharedRevs, opts.MinDegree = 1, 1, 0
	first := Analyze(commits, opts)
	for i := 0; i < 5; i++ {
		again := Analyze(commits, opts)
		if len(again.Pairs) != len(first.Pairs) {
			t.Fatalf("pair count unstable across runs")
		}
		for j := range first.Pairs {
			if again.Pairs[j].A != first.Pairs[j].A || again.Pairs[j].B != first.Pairs[j].B {
				t.Fatalf("order unstable at %d: %+v vs %+v", j, again.Pairs[j], first.Pairs[j])
			}
		}
	}
	// degree 降順が先、同値は path 昇順
	for i := 1; i < len(first.Pairs); i++ {
		prev, cur := first.Pairs[i-1], first.Pairs[i]
		if cur.Degree > prev.Degree+1e-12 {
			t.Errorf("pairs not sorted by degree desc at %d: %v > %v", i, cur.Degree, prev.Degree)
		}
	}
}

func TestAnalyze_EmptyInputIsNotAnError(t *testing.T) {
	rep := Analyze(nil, DefaultOptions())
	if rep.Valid {
		t.Error("empty history must not be reported as valid coupling data")
	}
	if len(rep.Pairs) != 0 {
		t.Errorf("want no pairs, got %d", len(rep.Pairs))
	}
	if rep.Note == "" {
		t.Error("must state why there is no result")
	}
}

func TestAnalyze_SingleFileCommitsProduceNoPairs(t *testing.T) {
	commits := []churn.Commit{
		commit(t, 1, "c1", "a.go"),
		commit(t, 2, "c2", "b.go"),
		commit(t, 3, "c3", "c.go"),
	}
	opts := DefaultOptions()
	opts.MinRevs, opts.MinSharedRevs, opts.MinDegree = 1, 1, 0
	rep := Analyze(commits, opts)
	if len(rep.Pairs) != 0 {
		t.Errorf("single-file commits cannot couple anything, got %d pairs", len(rep.Pairs))
	}
	if rep.Valid {
		t.Error("no pairs means there is nothing to report as valid")
	}
}

// sum-of-coupling: 「他のどれかと一緒に変わった回数の総和」。CodeScene が
// アーキテクチャ的な重要ファイルを surface するのに使う量。
func TestAnalyze_SumOfCouplingRanksTheHub(t *testing.T) {
	commits := []churn.Commit{
		commit(t, 1, "c1", "hub.go", "a.go"),
		commit(t, 2, "c2", "hub.go", "b.go"),
		commit(t, 3, "c3", "hub.go", "c.go"),
		commit(t, 4, "c4", "a.go", "b.go"),
	}
	opts := DefaultOptions()
	opts.MinRevs, opts.MinSharedRevs, opts.MinDegree = 1, 1, 0
	rep := Analyze(commits, opts)
	if len(rep.SumOfCoupling) == 0 {
		t.Fatal("want sum-of-coupling entries")
	}
	if got := rep.SumOfCoupling[0].Entity; got != "hub.go" {
		t.Errorf("top sum-of-coupling: want hub.go, got %s", got)
	}
	if got := rep.SumOfCoupling[0].SharedRevs; got != 3 {
		t.Errorf("hub shared revs: want 3, got %d", got)
	}
}

// confidence は基準率に交絡する: ほぼ毎コミットで変わるファイルは、どんな相手からも
// confidence ≈ 1.0 に見えるが、それは「常に変わっている」だけで情報が無い。
// association rule mining の標準的な補正が **lift(interest)** = P(B|A)/P(B)。
// Brin et al. (SIGMOD 1997) が confidence 単独の不足として指摘した点。
func TestAnalyze_LiftCorrectsForBaseRate(t *testing.T) {
	var commits []churn.Commit
	// ritual.go は 20 コミット全部で変わる(= 基準率 1.0)。
	// tight-a/tight-b は 6 回だけ、しかし必ず一緒に変わる。
	for i := 1; i <= 14; i++ {
		commits = append(commits, commit(t, i, "ritual", "ritual.go", "other.go"))
	}
	for i := 15; i <= 20; i++ {
		commits = append(commits, commit(t, i, "tight", "ritual.go", "tight-a.go", "tight-b.go"))
	}
	opts := DefaultOptions()
	opts.MinRevs, opts.MinSharedRevs, opts.MinDegree = 1, 1, 0
	rep := Analyze(commits, opts)

	byPair := map[string]Pair{}
	for _, p := range rep.Pairs {
		byPair[p.A+"|"+p.B] = p
	}
	ritual, ok := byPair["other.go|ritual.go"]
	if !ok {
		t.Fatalf("missing ritual pair; got %+v", rep.Pairs)
	}
	tight, ok := byPair["tight-a.go|tight-b.go"]
	if !ok {
		t.Fatalf("missing tight pair; got %+v", rep.Pairs)
	}
	// ritual.go は毎回変わるので other.go から見た confidence は 1.0。
	if math.Abs(ritual.ConfidenceAB-1.0) > 1e-9 {
		t.Errorf("P(ritual|other) should be 1.0, got %v", ritual.ConfidenceAB)
	}
	// しかし lift は 1.0 —— 何も教えてくれない。
	if math.Abs(ritual.Lift-1.0) > 1e-9 {
		t.Errorf("ritual pair lift should be ~1.0 (no information), got %v", ritual.Lift)
	}
	// 一方、稀だが固く結びついた対の lift は 1 より大きい。
	if tight.Lift <= 1.0 {
		t.Errorf("tight pair lift should exceed 1, got %v", tight.Lift)
	}
	// lift は **対称**(confidence と違い方向を持たない)——これも固定しておく。
	if math.Abs(tight.Lift-(tight.ConfidenceBA/(float64(tight.RevsA)/float64(rep.CommitsUsed)))) > 1e-9 {
		t.Errorf("lift must be symmetric: %v", tight.Lift)
	}
}

// 提案の並べ替え規則を選べること。既定は confidence、RankByLift で基準率補正版。
func TestEvaluate_RankByLiftDiffersFromConfidence(t *testing.T) {
	var train []churn.Commit
	// seed.go は必ず ritual.go と一緒に変わる(confidence 高)が、ritual.go は
	// どのみち毎回変わる(lift ≈ 1)。rare.go は seed.go とだけ結びつく(lift 高)。
	for i := 1; i <= 10; i++ {
		train = append(train, commit(t, i, "ritual", "seed.go", "ritual.go"))
	}
	for i := 11; i <= 16; i++ {
		train = append(train, commit(t, i, "both", "seed.go", "ritual.go", "rare.go"))
	}
	for i := 17; i <= 24; i++ {
		train = append(train, commit(t, i, "ritual only", "ritual.go", "misc.go"))
	}
	opts := DefaultOptions()
	opts.MinRevs, opts.MinSharedRevs, opts.MinDegree = 1, 1, 0

	rep := Analyze(train, opts)
	byConf := Suggest(rep, "seed.go", 1, false)
	byLift := Suggest(rep, "seed.go", 1, true)
	if len(byConf) != 1 || len(byLift) != 1 {
		t.Fatalf("want one suggestion each, got %v / %v", byConf, byLift)
	}
	if byConf[0] != "ritual.go" {
		t.Errorf("confidence ranking should pick the ever-changing file, got %s", byConf[0])
	}
	if byLift[0] != "rare.go" {
		t.Errorf("lift ranking should pick the informative file, got %s", byLift[0])
	}
}

func TestSplit_IsChronologicalAndRounds(t *testing.T) {
	// 入力は git log 順(新しい順)でも、split は時系列昇順で切る。
	commits := []churn.Commit{
		commit(t, 4, "newest", "d.go"),
		commit(t, 1, "oldest", "a.go"),
		commit(t, 3, "third", "c.go"),
		commit(t, 2, "second", "b.go"),
	}
	train, test := Split(commits, 0.75)
	if len(train) != 3 || len(test) != 1 {
		t.Fatalf("0.75 of 4 must be 3/1, got %d/%d", len(train), len(test))
	}
	if train[0].Subject != "oldest" || train[2].Subject != "third" {
		t.Errorf("train must be chronological, got %s..%s", train[0].Subject, train[2].Subject)
	}
	if test[0].Subject != "newest" {
		t.Errorf("test must hold the newest commit, got %s", test[0].Subject)
	}
	// 訓練は必ず検証より前(順序保存)
	if !train[len(train)-1].When.Before(test[0].When) {
		t.Error("train window must end before the test window begins")
	}
}

// Zimmermann らの評価形: 「最初の変更が与えられたとき、次に変わるファイルを
// 当てられるか」。結合構造があるなら頻度ベースラインを上回るはず。
func TestEvaluate_CouplingBeatsFrequencyBaselineWhenStructureExists(t *testing.T) {
	var train []churn.Commit
	// 構造: x.go と y.go は常にペア。noise.go は最も頻繁に変わるが誰とも組まない。
	for i := 1; i <= 8; i++ {
		train = append(train, commit(t, i, "pair", "x.go", "y.go"))
	}
	for i := 9; i <= 30; i++ {
		train = append(train, commit(t, i, "noise", "noise.go"))
	}
	test := []churn.Commit{commit(t, 40, "pair again", "x.go", "y.go")}

	opts := DefaultOptions()
	opts.MinRevs, opts.MinSharedRevs, opts.MinDegree = 1, 1, 0
	ev := Evaluate(train, test, opts, 1)
	if !ev.Valid {
		t.Fatalf("evaluation should be valid: %s", ev.Note)
	}
	if ev.Precision <= ev.BaselinePrecision {
		t.Errorf("coupling should beat the frequency baseline: %v vs %v", ev.Precision, ev.BaselinePrecision)
	}
	if ev.Lift <= 1 {
		t.Errorf("lift should exceed 1, got %v", ev.Lift)
	}
}

// 検証器は **落ちうる** ものでなければ何も検証しない。訓練の結合が検証期の
// 実態とずれていれば lift は 1 を下回るはずで、その場合もそのまま報告する。
func TestEvaluate_MisleadingCouplingScoresBelowBaseline(t *testing.T) {
	var train []churn.Commit
	// 訓練では a.go-b.go が固く結合。
	for i := 1; i <= 10; i++ {
		train = append(train, commit(t, i, "old pair", "a.go", "b.go"))
	}
	// 検証期には a.go は b.go ではなく z.go と組むようになった(結合が古い)。
	var test []churn.Commit
	for i := 20; i <= 24; i++ {
		test = append(test, commit(t, i, "new pair", "a.go", "z.go"))
	}
	opts := DefaultOptions()
	opts.MinRevs, opts.MinSharedRevs, opts.MinDegree = 1, 1, 0
	ev := Evaluate(train, test, opts, 1)
	if ev.Precision != 0 {
		t.Errorf("stale coupling must not score: precision %v", ev.Precision)
	}
	if ev.Lift >= 1 {
		t.Errorf("stale coupling must fall below the baseline, got lift %v", ev.Lift)
	}
}

func TestEvaluate_NoCouplingStructureIsInvalid(t *testing.T) {
	var train []churn.Commit
	for i := 1; i <= 10; i++ {
		train = append(train, commit(t, i, "solo", "a.go"))
	}
	test := []churn.Commit{commit(t, 20, "solo", "a.go", "b.go")}
	ev := Evaluate(train, test, DefaultOptions(), 3)
	if ev.Valid {
		t.Error("no rules learned means nothing was validated — must not report a score")
	}
	if ev.Note == "" {
		t.Error("must state why the evaluation is invalid")
	}
}

// 提案が出せなかったケースを黙って「外れ」に混ぜると precision が沈み、
// 黙って除外すると precision が浮く。どちらも嘘なので coverage を併記する。
func TestEvaluate_ReportsCoverageForCasesWithoutSuggestions(t *testing.T) {
	var train []churn.Commit
	for i := 1; i <= 6; i++ {
		train = append(train, commit(t, i, "pair", "x.go", "y.go"))
	}
	test := []churn.Commit{
		commit(t, 20, "known", "x.go", "y.go"),
		commit(t, 21, "unknown", "p.go", "q.go"), // 訓練に一切現れない
	}
	opts := DefaultOptions()
	opts.MinRevs, opts.MinSharedRevs, opts.MinDegree = 1, 1, 0
	ev := Evaluate(train, test, opts, 1)
	if ev.Cases != 4 {
		t.Errorf("cases: want 4 (2 commits x 2 files), got %d", ev.Cases)
	}
	if ev.CasesWithSuggestion != 2 {
		t.Errorf("cases with a suggestion: want 2, got %d", ev.CasesWithSuggestion)
	}
	if ev.Coverage <= 0 || ev.Coverage >= 1 {
		t.Errorf("coverage must be strictly between 0 and 1 here, got %v", ev.Coverage)
	}
}

func TestEvaluate_NoteNamesTheProtocolAndItsLimits(t *testing.T) {
	var train []churn.Commit
	for i := 1; i <= 6; i++ {
		train = append(train, commit(t, i, "pair", "x.go", "y.go"))
	}
	test := []churn.Commit{commit(t, 20, "pair", "x.go", "y.go")}
	opts := DefaultOptions()
	opts.MinRevs, opts.MinSharedRevs, opts.MinDegree = 1, 1, 0
	ev := Evaluate(train, test, opts, 1)
	for _, want := range []string{"precision@", "baseline", "coverage"} {
		if !strings.Contains(ev.Note, want) {
			t.Errorf("note must mention %q; got %q", want, ev.Note)
		}
	}
}

// limit は **表示用** の絞り込みであって測定条件ではない。live dogfood で
// `limit=5` を渡したら検証に使う規則まで 5 本に削られ、precision も baseline も
// 別の数字になっていた——測定が呼び出し側の表示都合で変わってはならない。
func TestEvaluate_LimitDoesNotChangeTheMeasurement(t *testing.T) {
	var train []churn.Commit
	for i := 1; i <= 10; i++ {
		train = append(train, commit(t, i, "pair", "x.go", "y.go"))
	}
	for i := 11; i <= 20; i++ {
		train = append(train, commit(t, i, "other", "p.go", "q.go"))
	}
	test := []churn.Commit{
		commit(t, 30, "t1", "x.go", "y.go"),
		commit(t, 31, "t2", "p.go", "q.go"),
	}
	base := DefaultOptions()
	base.MinRevs, base.MinSharedRevs, base.MinDegree = 1, 1, 0

	full := Evaluate(train, test, base, 1)
	limited := base
	limited.Limit = 1
	got := Evaluate(train, test, limited, 1)

	if got.Rules != full.Rules {
		t.Errorf("limit changed the rule count used for validation: %d vs %d", got.Rules, full.Rules)
	}
	if got.Precision != full.Precision || got.BaselinePrecision != full.BaselinePrecision {
		t.Errorf("limit changed the measurement: precision %v/%v baseline %v/%v",
			got.Precision, full.Precision, got.BaselinePrecision, full.BaselinePrecision)
	}
}
