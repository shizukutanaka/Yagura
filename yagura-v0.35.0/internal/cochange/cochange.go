// Package cochange は git 履歴から **進化的結合(evolutionary / logical /
// temporal coupling)** ——「一緒に変わるファイルはどれか」——を算出し、
// その結合が **将来の共変更を実際に当てられるか** を時系列分割で検証する
// (v0.128.0)。
//
// なぜ内部に既にある internal/coupling とは別物か:
//
//	`internal/coupling` は **静的な import 結合**(fan-in / fan-out / instability)を
//	ソースから測る。本パッケージが測るのは **履歴上の共変更**。両者は一致しない——
//	そして一致しない箇所こそ重要で、import 関係が無いのに必ず一緒に変わるファイル対は
//	コンパイラにも型検査にも見えない暗黙の契約を共有している。behavioral code
//	analysis の中心的主張がこれである。名前が近いので混同しないこと(本パッケージは
//	git を読み、`internal/coupling` はソースを読む)。
//
// 研究的根拠:
//
//	Gall, Hajek & Jazayeri, "Detection of Logical Coupling Based on Product Release
//	History", ICSM 1998 — リリース履歴の共変更から、構造解析では見えない論理的結合を
//	検出するという着想の出発点。
//
//	Zimmermann, Weißgerber, Diehl & Zeller, "Mining Version Histories to Guide
//	Software Changes", ICSE 2004 / IEEE TSE 31(6) 2005(ROSE)——版履歴に
//	association rule mining を適用し、変更中のプログラマへ「次に変えるべき場所」を
//	提案する。報告値は **最初の変更の後に further changes のファイルを 26% 正しく予測**、
//	関数・変数レベルの精度は 15%、**上位 3 提案のいずれかが正解を含む確率 64%**。
//	本パッケージの Evaluate はこの「最初の変更を与えて次を当てる」評価形を踏襲する。
//
// 関連ソフトウェア(同じ量を実装している既存ツール):
//
//	code-maat(Adam Tornhill)の coupling 解析は entity / coupled / degree(百分率)/
//	average-revs を出し、既定値は --min-revs 5 / --min-shared-revs 5 /
//	--min-coupling 30 / --max-changeset-size 30。本パッケージの既定値はこれに揃えた
//	——特に **changeset size 上限**が重要で、一括フォーマットや license header 更新の
//	ような巨大コミットは 1 個で N*(N-1)/2 個の偽の結合を生む。CodeScene は同じ考えを
//	製品化し sum-of-coupling(「他と一緒に変わった回数の総和」)でアーキテクチャ上
//	重要なファイルを surface する——本パッケージも併せて出す。
//
// **degree と confidence は別の量として両方出す**(honest capability):
//
//	code-maat 系の degree は shared / average(revs) の **対称** 値。association rule の
//	confidence は shared / revs(A) の **方向つき** 値で、P(B が変わる | A が変わる)。
//	A が 10 回・B が 2 回変わって共変更 2 回なら、P(A|B)=1.0 だが P(B|A)=0.2 ——
//	対称値 1 本だけ出すと読み手はこれを「A を触ったら B も触る確率」と誤読する。
//	よって Pair は degree・ConfidenceAB・ConfidenceBA を並べて返す。
//
// 検証について:
//
//	Evaluate は **時系列分割**(Split で古い側=訓練、新しい側=検証)を要求し、
//	検証期の各コミットについて「1 ファイルを与えて残りを当てる」precision@K を
//	**頻度ベースライン**(訓練期で最も多く変わった K ファイルを常に提案する)と
//	併記する。ベースラインなしの precision は意味を持たない——「よく変わるファイルを
//	挙げるだけ」で当たってしまうため。提案を出せなかったケースは coverage として
//	別に数える(黙って外れに混ぜると precision が沈み、黙って捨てると浮く)。
//
// zero-dep(ADR-0001): stdlib + internal/churn のみ。
package cochange

import (
	"fmt"
	"math"
	"sort"

	"github.com/shizukutanaka/yagura/internal/churn"
)

// 既定値は code-maat の CLI 既定に揃える(上記 package doc 参照)。
const (
	DefaultMaxChangesetFiles = 30
	DefaultMinRevs           = 5
	DefaultMinSharedRevs     = 5
	DefaultMinDegree         = 0.30
)

// Options は解析のしきい値。
type Options struct {
	// MaxChangesetFiles を超えるファイル数のコミットは **結合の算出に使わない**。
	// 一括変更は 1 コミットで N*(N-1)/2 個の偽の結合を作るため。
	MaxChangesetFiles int `json:"max_changeset_files"`
	// MinRevs 未満しか変わっていないファイルは対象外(統計的に語れない)。
	MinRevs int `json:"min_revs"`
	// MinSharedRevs 未満の共変更しかない対は対象外。
	MinSharedRevs int `json:"min_shared_revs"`
	// MinDegree 未満の対称結合度の対は対象外(0-1)。
	MinDegree float64 `json:"min_degree"`
	// Limit > 0 で返す対の数を上限する(0 = 全件)。
	Limit int `json:"limit"`
	// RankByLift は提案の並べ替えを confidence(既定)ではなく lift で行う。
	// 基準率交絡(毎回変わるファイルが常に上位に来る)を補正したい場合に使う。
	RankByLift bool `json:"rank_by_lift"`
}

// DefaultOptions は code-maat 既定に揃えたしきい値を返す。
func DefaultOptions() Options {
	return Options{
		MaxChangesetFiles: DefaultMaxChangesetFiles,
		MinRevs:           DefaultMinRevs,
		MinSharedRevs:     DefaultMinSharedRevs,
		MinDegree:         DefaultMinDegree,
	}
}

// Pair は 1 つのファイル対の結合。A < B(path 昇順)で正規化される。
type Pair struct {
	A          string `json:"a"`
	B          string `json:"b"`
	SharedRevs int    `json:"shared_revs"`
	RevsA      int    `json:"revs_a"`
	RevsB      int    `json:"revs_b"`
	// AverageRevs は (RevsA+RevsB)/2。code-maat の average-revs 列に相当。
	AverageRevs float64 `json:"average_revs"`
	// Degree は **対称** 結合度 = SharedRevs / AverageRevs(0-1)。
	Degree float64 `json:"degree"`
	// ConfidenceAB は P(B が変わる | A が変わる) = SharedRevs / RevsA。**方向つき**。
	ConfidenceAB float64 `json:"confidence_a_to_b"`
	// ConfidenceBA は P(A が変わる | B が変わる) = SharedRevs / RevsB。
	ConfidenceBA float64 `json:"confidence_b_to_a"`
	// Lift は基準率で補正した interest = P(A,B) / (P(A)*P(B))。
	//
	// confidence だけでは「相手がどのみち毎回変わるファイル」を高く評価してしまう
	// (基準率交絡)。lift = 1 は独立 = 情報なし、> 1 で偶然より一緒に変わる。
	// confidence と違い lift は **対称** である(P(B|A)/P(B) = P(A|B)/P(A))。
	Lift float64 `json:"lift"`
}

// EntityCoupling は sum-of-coupling(他のどれかと一緒に変わった回数の総和)。
type EntityCoupling struct {
	Entity     string `json:"entity"`
	SharedRevs int    `json:"shared_revs"` // 全パートナーとの共変更数の総和
	Partners   int    `json:"partners"`    // 結合相手の数
}

// Report は進化的結合の解析結果。
type Report struct {
	Valid               bool             `json:"valid"`
	Pairs               []Pair           `json:"pairs"`
	SumOfCoupling       []EntityCoupling `json:"sum_of_coupling"`
	Commits             int              `json:"commits"`
	CommitsUsed         int              `json:"commits_used"`
	CommitsSkippedLarge int              `json:"commits_skipped_large"`
	CommitsSingleFile   int              `json:"commits_single_file"`
	Entities            int              `json:"entities"`
	Options             Options          `json:"options"`
	Note                string           `json:"note"`
}

const analyzeNote = "Evolutionary (logical) coupling from git history: how often two files change " +
	"in the same commit. Gall et al. (ICSM 1998) introduced detecting logical coupling from release " +
	"history; Zimmermann et al. (ICSE 2004 / TSE 2005, ROSE) mined it as association rules to " +
	"suggest further changes. `degree` is SYMMETRIC (shared/average-revs, the code-maat convention); " +
	"`confidence_a_to_b` and `confidence_b_to_a` are DIRECTIONAL and generally differ — a file that " +
	"changes rarely can imply a busy file with confidence 1.0 while the reverse is near zero. " +
	"This is NOT internal/coupling, which measures static import coupling from source; a pair that " +
	"couples here without importing each other shares an implicit contract no compiler can see."

// pairKey は正規化済みのファイル対キー。
type pairKey struct{ a, b string }

// Analyze は commits から進化的結合を算出する。
//
// 入力の順序は問わない(集計は順序に依存しない)。git 履歴が無い / 対が 1 つも
// 残らない場合は Valid=false と理由を返す——空の結果を「結合なし=健全」と
// 読ませない。
func Analyze(commits []churn.Commit, opts Options) Report {
	opts = normalize(opts)
	rep := Report{Pairs: []Pair{}, SumOfCoupling: []EntityCoupling{}, Commits: len(commits), Options: opts}
	if len(commits) == 0 {
		rep.Note = "no commit history: cannot derive evolutionary coupling"
		return rep
	}

	revs := map[string]int{}
	shared := map[pairKey]int{}
	for _, c := range commits {
		paths := uniquePaths(c.Files)
		if len(paths) == 0 {
			continue
		}
		if len(paths) > opts.MaxChangesetFiles {
			// 一括変更は結合の算出から外す。revs にも数えない——分母だけ増えると
			// degree が不当に下がり、同じコミットを二重に扱うことになる。
			rep.CommitsSkippedLarge++
			continue
		}
		rep.CommitsUsed++
		if len(paths) == 1 {
			rep.CommitsSingleFile++
		}
		for _, p := range paths {
			revs[p]++
		}
		for i := 0; i < len(paths); i++ {
			for j := i + 1; j < len(paths); j++ {
				shared[pairKey{paths[i], paths[j]}]++
			}
		}
	}
	rep.Entities = len(revs)

	keys := make([]pairKey, 0, len(shared))
	for k := range shared {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].a != keys[j].a {
			return keys[i].a < keys[j].a
		}
		return keys[i].b < keys[j].b
	})

	soc := map[string]*EntityCoupling{}
	for _, k := range keys {
		n := shared[k]
		ra, rb := revs[k.a], revs[k.b]
		if n < opts.MinSharedRevs || ra < opts.MinRevs || rb < opts.MinRevs {
			continue
		}
		avg := float64(ra+rb) / 2
		if avg == 0 {
			continue
		}
		degree := float64(n) / avg
		if degree < opts.MinDegree {
			continue
		}
		rep.Pairs = append(rep.Pairs, Pair{
			A: k.a, B: k.b, SharedRevs: n, RevsA: ra, RevsB: rb,
			AverageRevs:  avg,
			Degree:       degree,
			ConfidenceAB: ratio(n, ra),
			ConfidenceBA: ratio(n, rb),
			Lift:         liftOf(n, ra, rb, rep.CommitsUsed),
		})
		addSOC(soc, k.a, n)
		addSOC(soc, k.b, n)
	}

	// degree 降順 → shared 降順 → path 昇順(決定論的)
	sort.SliceStable(rep.Pairs, func(i, j int) bool {
		x, y := rep.Pairs[i], rep.Pairs[j]
		if math.Abs(x.Degree-y.Degree) > 1e-12 {
			return x.Degree > y.Degree
		}
		if x.SharedRevs != y.SharedRevs {
			return x.SharedRevs > y.SharedRevs
		}
		if x.A != y.A {
			return x.A < y.A
		}
		return x.B < y.B
	})
	if opts.Limit > 0 && len(rep.Pairs) > opts.Limit {
		rep.Pairs = rep.Pairs[:opts.Limit]
	}

	rep.SumOfCoupling = rankSOC(soc)
	if len(rep.Pairs) == 0 {
		rep.Note = fmt.Sprintf("no file pair met the thresholds (min_shared_revs=%d, min_revs=%d, "+
			"min_degree=%.2f) across %d commit(s); %d commit(s) touched a single file and %d exceeded "+
			"max_changeset_files=%d. Absence of coupling here means the thresholds found nothing, "+
			"NOT that the code is decoupled.",
			opts.MinSharedRevs, opts.MinRevs, opts.MinDegree, rep.Commits,
			rep.CommitsSingleFile, rep.CommitsSkippedLarge, opts.MaxChangesetFiles)
		return rep
	}
	rep.Valid = true
	rep.Note = analyzeNote
	return rep
}

// Split は commits を時系列昇順に並べ、古い側 ratio を訓練、残りを検証として返す。
//
// 分割点は **四捨五入**(truncation は要求した比率と違う窓を静かに作る——
// internal/defectdataset で実際に踏んだ罠)。
func Split(commits []churn.Commit, ratio float64) (train, test []churn.Commit) {
	ordered := append([]churn.Commit(nil), commits...)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].When.Before(ordered[j].When) })
	if len(ordered) == 0 {
		return nil, nil
	}
	if ratio <= 0 || ratio >= 1 {
		return ordered, nil
	}
	cut := int(math.Round(float64(len(ordered)) * ratio))
	if cut < 1 {
		cut = 1
	}
	if cut > len(ordered)-1 {
		cut = len(ordered) - 1
	}
	return ordered[:cut], ordered[cut:]
}

// Evaluation は「最初の変更から次の変更を当てる」評価の結果。
type Evaluation struct {
	Valid bool `json:"valid"`
	K     int  `json:"k"`
	// RankedBy は提案の並べ替えに使った量("confidence" | "lift")。
	RankedBy     string `json:"ranked_by"`
	TrainCommits int    `json:"train_commits"`
	TestCommits  int    `json:"test_commits"`
	Rules        int    `json:"rules"` // 訓練期から学べた対の数
	// Cases は検証期の (コミット, 起点ファイル) の総数(複数ファイルのコミットのみ)。
	Cases int `json:"cases"`
	// CasesWithSuggestion は起点ファイルに対し提案を出せたケース数。
	CasesWithSuggestion int `json:"cases_with_suggestion"`
	// Coverage = CasesWithSuggestion / Cases。提案が出せない率を隠さないため。
	Coverage          float64 `json:"coverage"`
	Hits              int     `json:"hits"`
	Suggestions       int     `json:"suggestions"`
	Precision         float64 `json:"precision_at_k"`
	BaselineHits      int     `json:"baseline_hits"`
	BaselinePrecision float64 `json:"baseline_precision_at_k"`
	// Lift = Precision / BaselinePrecision。ベースラインが 1 件も当てられなかった
	// 場合、比は **定義できない**。以前はここに +Inf を入れていたが、+Inf は JSON の
	// 数値ではないため encoding/json が構造体ごと落ち、`yagura_change_coupling` の
	// 応答が丸ごと消えていた(hugo を k=1 で測って発見)。定義できない量は
	// nil = null で表し、理由は Note に書く。捏造した有限値も、黙った 0 も置かない。
	Lift *float64 `json:"lift"`
	Note string   `json:"note"`
}

const evalNote = "Temporal evaluation in the shape of Zimmermann et al. (ICSE 2004 / TSE 2005): " +
	"rules are mined from the TRAIN window only, then for each file in each multi-file commit of the " +
	"TEST window the top-K coupled partners are suggested and scored against the files that actually " +
	"changed alongside it. precision@K is reported against a FREQUENCY baseline that always suggests " +
	"the K most-changed files of the train window — without that baseline a precision number means " +
	"nothing, because naming busy files alone scores hits. Cases where no rule covered the seed file " +
	"are counted in coverage rather than silently dropped or silently scored as misses. NOTE: this is " +
	"file-level and single-seed; ROSE's published figures (26% of further files, 64% for the topmost " +
	"three suggestions) come from a different corpus and granularity and are NOT a target to match."

// Evaluate は train から結合規則を学び、test の共変更を当てられるかを測る。
//
// train / test の時系列順は **呼び出し側の責任**(Split を使うこと)。規則が
// 1 つも学べない場合は Valid=false を返す——常に成功する検証器は何も検証しない。
func Evaluate(train, test []churn.Commit, opts Options, k int) Evaluation {
	opts = normalize(opts)
	// Limit は **表示用**(返す対の数)であって測定条件ではない。ここで落とさないと
	// 「上位 N 件だけ見せて」という呼び出しが、検証に使う規則の数まで黙って削り、
	// precision も baseline も変えてしまう(v0.128.0 の live dogfood で発見)。
	opts.Limit = 0
	if k <= 0 {
		k = 3
	}
	ev := Evaluation{K: k, TrainCommits: len(train), TestCommits: len(test), RankedBy: "confidence"}
	if opts.RankByLift {
		ev.RankedBy = "lift"
	}

	rep := Analyze(train, opts)
	ev.Rules = len(rep.Pairs)
	if ev.Rules == 0 {
		ev.Note = "no coupling rules could be mined from the train window (" + rep.Note + ")"
		return ev
	}

	partners := buildPartners(rep.Pairs, opts.RankByLift)
	baseline := topChanged(train, opts, k+1) // seed 自身を除外するため 1 個多めに持つ

	for _, c := range test {
		paths := uniquePaths(c.Files)
		if len(paths) < 2 || len(paths) > opts.MaxChangesetFiles {
			continue
		}
		actual := make(map[string]bool, len(paths))
		for _, p := range paths {
			actual[p] = true
		}
		for _, seed := range paths {
			ev.Cases++
			sugg := suggest(partners[seed], seed, k)
			if len(sugg) == 0 {
				continue
			}
			ev.CasesWithSuggestion++
			ev.Suggestions += len(sugg)
			for _, s := range sugg {
				if actual[s] {
					ev.Hits++
				}
			}
			// ベースラインは **同じケース集合・同じ提案数** で測る(条件を揃えないと
			// 比較にならない)。
			for _, s := range pick(baseline, seed, len(sugg)) {
				if actual[s] {
					ev.BaselineHits++
				}
			}
		}
	}

	if ev.Cases > 0 {
		ev.Coverage = float64(ev.CasesWithSuggestion) / float64(ev.Cases)
	}
	if ev.Suggestions == 0 {
		ev.Note = "the train window produced rules, but none of them covered any file changed in the " +
			"test window; nothing could be scored"
		return ev
	}
	ev.Precision = float64(ev.Hits) / float64(ev.Suggestions)
	ev.BaselinePrecision = float64(ev.BaselineHits) / float64(ev.Suggestions)
	if ev.BaselinePrecision > 0 {
		lift := ev.Precision / ev.BaselinePrecision
		ev.Lift = &lift
	}
	ev.Valid = true
	ev.Note = evalNote
	if ev.Lift == nil {
		ev.Note += " The frequency baseline scored zero hits, so lift is undefined (null) rather " +
			"than a finite ratio; read the precision and baseline values directly."
	}
	return ev
}

// --- helpers ---

func normalize(o Options) Options {
	if o.MaxChangesetFiles <= 0 {
		o.MaxChangesetFiles = DefaultMaxChangesetFiles
	}
	if o.MinRevs <= 0 {
		o.MinRevs = 1
	}
	if o.MinSharedRevs <= 0 {
		o.MinSharedRevs = 1
	}
	if o.MinDegree < 0 {
		o.MinDegree = 0
	}
	return o
}

// uniquePaths は 1 コミット内の重複パスを潰して昇順で返す(同一パスが複数行に
// 現れても 1 回だけ数える)。
func uniquePaths(files []churn.FileChange) []string {
	if len(files) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(files))
	out := make([]string, 0, len(files))
	for _, f := range files {
		if f.Path == "" || seen[f.Path] {
			continue
		}
		seen[f.Path] = true
		out = append(out, f.Path)
	}
	sort.Strings(out)
	return out
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return float64(n) / float64(d)
}

// liftOf は P(A,B)/(P(A)P(B)) を返す。全確率の分母は「結合の算出に使った
// コミット数」で揃える(revs も shared も同じ母集団で数えているため)。
func liftOf(shared, revsA, revsB, commitsUsed int) float64 {
	if commitsUsed == 0 || revsA == 0 || revsB == 0 {
		return 0
	}
	pAB := float64(shared) / float64(commitsUsed)
	pA := float64(revsA) / float64(commitsUsed)
	pB := float64(revsB) / float64(commitsUsed)
	return pAB / (pA * pB)
}

// Suggest は「seed を触ったとき次に見るべきファイル」を上位 k 件返す。
//
// rankByLift=false は confidence(P(partner|seed))順、true は lift 順。
// confidence 順は「どのみち毎回変わるファイル」を上位に押し上げるため、
// 基準率を補正したい場合は lift 順を使う——どちらが良いかはリポジトリ次第なので
// 選ばせる(既定を黙って決め打ちしない)。
func Suggest(rep Report, seed string, k int, rankByLift bool) []string {
	if k <= 0 {
		return nil
	}
	return suggest(buildPartners(rep.Pairs, rankByLift)[seed], seed, k)
}

func addSOC(m map[string]*EntityCoupling, entity string, shared int) {
	e, ok := m[entity]
	if !ok {
		e = &EntityCoupling{Entity: entity}
		m[entity] = e
	}
	e.SharedRevs += shared
	e.Partners++
}

func rankSOC(m map[string]*EntityCoupling) []EntityCoupling {
	out := make([]EntityCoupling, 0, len(m))
	for _, e := range m {
		out = append(out, *e)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].SharedRevs != out[j].SharedRevs {
			return out[i].SharedRevs > out[j].SharedRevs
		}
		if out[i].Partners != out[j].Partners {
			return out[i].Partners > out[j].Partners
		}
		return out[i].Entity < out[j].Entity
	})
	return out
}

// suggestion は 1 件の提案候補。
type suggestion struct {
	path   string
	score  float64
	shared int
}

// buildPartners は「起点ファイル → スコア降順のパートナー列」を作る。
//
// rankByLift=false のスコアは **方向つき** confidence(起点から見た条件付き確率で
// なければ提案の順序が意味を持たない)。true なら対称 lift(基準率補正版)。
func buildPartners(pairs []Pair, rankByLift bool) map[string][]suggestion {
	m := map[string][]suggestion{}
	for _, p := range pairs {
		sa, sb := p.ConfidenceAB, p.ConfidenceBA
		if rankByLift {
			sa, sb = p.Lift, p.Lift
		}
		m[p.A] = append(m[p.A], suggestion{path: p.B, score: sa, shared: p.SharedRevs})
		m[p.B] = append(m[p.B], suggestion{path: p.A, score: sb, shared: p.SharedRevs})
	}
	for seed := range m {
		list := m[seed]
		sort.SliceStable(list, func(i, j int) bool {
			if math.Abs(list[i].score-list[j].score) > 1e-12 {
				return list[i].score > list[j].score
			}
			if list[i].shared != list[j].shared {
				return list[i].shared > list[j].shared
			}
			return list[i].path < list[j].path
		})
		m[seed] = list
	}
	return m
}

func suggest(list []suggestion, seed string, k int) []string {
	out := make([]string, 0, k)
	for _, s := range list {
		if s.path == seed {
			continue
		}
		out = append(out, s.path)
		if len(out) == k {
			break
		}
	}
	return out
}

// topChanged は訓練期で変更回数の多いファイルを降順で返す(頻度ベースライン)。
func topChanged(commits []churn.Commit, opts Options, n int) []string {
	revs := map[string]int{}
	for _, c := range commits {
		paths := uniquePaths(c.Files)
		if len(paths) == 0 || len(paths) > opts.MaxChangesetFiles {
			continue
		}
		for _, p := range paths {
			revs[p]++
		}
	}
	out := make([]string, 0, len(revs))
	for p := range revs {
		out = append(out, p)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if revs[out[i]] != revs[out[j]] {
			return revs[out[i]] > revs[out[j]]
		}
		return out[i] < out[j]
	})
	if n > 0 && len(out) > n {
		out = out[:n]
	}
	return out
}

func pick(candidates []string, exclude string, n int) []string {
	out := make([]string, 0, n)
	for _, c := range candidates {
		if c == exclude {
			continue
		}
		out = append(out, c)
		if len(out) == n {
			break
		}
	}
	return out
}
