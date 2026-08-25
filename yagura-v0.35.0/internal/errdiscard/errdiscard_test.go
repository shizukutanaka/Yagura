package errdiscard_test

import (
	"testing"

	"github.com/shizukutanaka/yagura/internal/errdiscard"
)

func TestScan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		files            map[string]string
		wantDiscarded    int
		wantCallsScanned int
		wantCallerName   string // first finding's Caller (if any)
		wantCalleeName   string // first finding's Callee (if any)
	}{
		{
			// 1. BasicDiscard: ExprStmt で error を捨てる
			name: "BasicDiscard",
			files: map[string]string{
				"a.go": `package a
func write() error { return nil }
func main() { write() }
`,
			},
			wantDiscarded:    1,
			wantCallsScanned: 1,
			wantCallerName:   "main",
			wantCalleeName:   "write",
		},
		{
			// 2. NoDiscard: IfStmt init は ExprStmt ではない
			name: "NoDiscard",
			files: map[string]string{
				"a.go": `package a
func write() error { return nil }
func main() { if err := write(); err != nil {} }
`,
			},
			wantDiscarded:    0,
			wantCallsScanned: 0,
		},
		{
			// 3. AssignedErr: AssignStmt は ExprStmt でない
			name: "AssignedErr",
			files: map[string]string{
				"a.go": `package a
func write() error { return nil }
func main() {
	err := write()
	_ = err
}
`,
			},
			wantDiscarded:    0,
			wantCallsScanned: 0,
		},
		{
			// 4. BlankAssign: _ = write() は AssignStmt、ユーザーの明示的選択
			name: "BlankAssign",
			files: map[string]string{
				"a.go": `package a
func write() error { return nil }
func main() { _ = write() }
`,
			},
			wantDiscarded:    0,
			wantCallsScanned: 0,
		},
		{
			// 5. MultiReturnLastError: (int, error) を返す関数を ExprStmt で呼ぶ
			name: "MultiReturnLastError",
			files: map[string]string{
				"a.go": `package a
func compute() (int, error) { return 0, nil }
func main() { compute() }
`,
			},
			wantDiscarded:    1,
			wantCallsScanned: 1,
			wantCallerName:   "main",
			wantCalleeName:   "compute",
		},
		{
			// 6. ReturnValueUsed: := で受け取る場合は AssignStmt
			name: "ReturnValueUsed",
			files: map[string]string{
				"a.go": `package a
func write2() (int, error) { return 0, nil }
func main() {
	x, err := write2()
	_ = x
	_ = err
}
`,
			},
			wantDiscarded:    0,
			wantCallsScanned: 0,
		},
		{
			// 7. TestFileSkipped: _test.go はスキップ
			name: "TestFileSkipped",
			files: map[string]string{
				"foo_test.go": `package foo_test
func write() error { return nil }
func TestDiscard(t *testing.T) { write() }
`,
			},
			wantDiscarded:    0,
			wantCallsScanned: 0,
		},
		{
			// 8. CallerNameRecorded: finding の Caller が正しい
			name: "CallerNameRecorded",
			files: map[string]string{
				"a.go": `package a
func doWork() error { return nil }
func runTask() { doWork() }
`,
			},
			wantDiscarded:  1,
			wantCallerName: "runTask",
			wantCalleeName: "doWork",
		},
		{
			// 9. CalleeNameRecorded: finding の Callee が正しい
			name: "CalleeNameRecorded",
			files: map[string]string{
				"a.go": `package a
func setupDB() error { return nil }
func init() { setupDB() }
`,
			},
			wantDiscarded:  1,
			wantCalleeName: "setupDB",
		},
		{
			// 10. NonErrorReturn: error でない戻り値を捨てるのは問題なし
			name: "NonErrorReturn",
			files: map[string]string{
				"a.go": `package a
func greet() string { return "hi" }
func main() { greet() }
`,
			},
			wantDiscarded:    0,
			wantCallsScanned: 1, // ExprStmt CallExpr は数える
		},
		{
			// 11. MultipleFindings: 3 箇所でエラーを捨てる
			name: "MultipleFindings",
			files: map[string]string{
				"a.go": `package a
func step() error { return nil }
func run() {
	step()
	step()
	step()
}
`,
			},
			wantDiscarded:    3,
			wantCallsScanned: 3,
		},
		{
			// 12. Deterministic: 2 ファイル、ソート確認
			name: "Deterministic",
			files: map[string]string{
				"b.go": `package p
func sink() error { return nil }
func bFunc() { sink() }
`,
				"a.go": `package p
func sink() error { return nil }
func aFunc() { sink() }
`,
			},
			wantDiscarded:    2,
			wantCallsScanned: 2,
		},
		{
			// 13. ParseError: 壊れた Go はスキップ、クラッシュしない
			name: "ParseError",
			files: map[string]string{
				"broken.go": `package broken
this is not valid Go code !!!
`,
			},
			wantDiscarded:    0,
			wantCallsScanned: 0,
		},
		{
			// 14. NonGoSkipped: .txt ファイルはスキップ
			name: "NonGoSkipped",
			files: map[string]string{
				"notes.txt": `func write() error { return nil }
func main() { write() }
`,
			},
			wantDiscarded:    0,
			wantCallsScanned: 0,
		},
		{
			// 15. CallsScannedCount: non-error ExprStmt calls も CallsScanned に含む
			name: "CallsScannedCount",
			files: map[string]string{
				"a.go": `package a
func errFunc() error { return nil }
func noErrFunc() string { return "" }
func main() {
	errFunc()    // discards error → counts
	noErrFunc()  // no error → counts in CallsScanned but not discarded
}
`,
			},
			wantDiscarded:    1,
			wantCallsScanned: 2,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := errdiscard.Scan(tc.files)
			if r.ErrorsDiscarded != tc.wantDiscarded {
				t.Errorf("ErrorsDiscarded: got %d, want %d", r.ErrorsDiscarded, tc.wantDiscarded)
			}
			if tc.wantCallsScanned > 0 && r.CallsScanned != tc.wantCallsScanned {
				t.Errorf("CallsScanned: got %d, want %d", r.CallsScanned, tc.wantCallsScanned)
			}
			if tc.wantCallerName != "" {
				if len(r.Findings) == 0 {
					t.Fatal("expected at least one finding for CallerName check")
				}
				if r.Findings[0].Caller != tc.wantCallerName {
					t.Errorf("Findings[0].Caller: got %q, want %q", r.Findings[0].Caller, tc.wantCallerName)
				}
			}
			if tc.wantCalleeName != "" {
				if len(r.Findings) == 0 {
					t.Fatal("expected at least one finding for CalleeName check")
				}
				if r.Findings[0].Callee != tc.wantCalleeName {
					t.Errorf("Findings[0].Callee: got %q, want %q", r.Findings[0].Callee, tc.wantCalleeName)
				}
			}
		})
	}
}

// TestDeterministic は結果が File→Line でソートされることを確認する。
func TestDeterministic(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"z.go": `package p
func sink() error { return nil }
func z() { sink() }
`,
		"a.go": `package p
func sink() error { return nil }
func a() { sink() }
`,
	}
	r1 := errdiscard.Scan(files)
	r2 := errdiscard.Scan(files)
	if len(r1.Findings) != len(r2.Findings) {
		t.Fatalf("non-deterministic finding count: %d vs %d", len(r1.Findings), len(r2.Findings))
	}
	for i := range r1.Findings {
		if r1.Findings[i].File != r2.Findings[i].File || r1.Findings[i].Line != r2.Findings[i].Line {
			t.Errorf("non-deterministic at index %d: %+v vs %+v", i, r1.Findings[i], r2.Findings[i])
		}
	}
	// ソート順: a.go が z.go より先
	if len(r1.Findings) >= 2 {
		if r1.Findings[0].File > r1.Findings[1].File {
			t.Errorf("findings not sorted by file: %q > %q", r1.Findings[0].File, r1.Findings[1].File)
		}
	}
}

// TestFindingFields は Finding の各フィールドが正しく設定されることを確認する。
func TestFindingFields(t *testing.T) {
	t.Parallel()
	files := map[string]string{
		"x.go": `package x
func cleanup() error { return nil }
func process() {
	cleanup()
}
`,
	}
	r := errdiscard.Scan(files)
	if len(r.Findings) != 1 {
		t.Fatalf("expected 1 finding, got %d", len(r.Findings))
	}
	f := r.Findings[0]
	if f.Rule != "errdiscard" {
		t.Errorf("Rule: got %q, want %q", f.Rule, "errdiscard")
	}
	if f.Severity != "medium" {
		t.Errorf("Severity: got %q, want %q", f.Severity, "medium")
	}
	if f.File != "x.go" {
		t.Errorf("File: got %q, want %q", f.File, "x.go")
	}
	if f.Caller != "process" {
		t.Errorf("Caller: got %q, want %q", f.Caller, "process")
	}
	if f.Callee != "cleanup" {
		t.Errorf("Callee: got %q, want %q", f.Callee, "cleanup")
	}
	if f.Message == "" {
		t.Error("Message should not be empty")
	}
}

// 名前だけの一致で「error を捨てている」と断定しない(v1.3.0)。
//
// 実際に起きていた誤検出:
//
//	内部に `func (s *Store) Set(...) error` が在るだけで、**まったく無関係な**
//	`w.Header().Set(...)`(戻り値なし)や `gauge.Set(1)`(戻り値なし)まで
//	「error を捨てている」と報告していた。同様に `atomicfile.Write` の存在が
//	`buf.Write(...)` を、`audit.Logger.Close` の存在が `conn.Close()` を巻き込んだ。
//	自リポジトリ 107 件の指摘の大半がこれだった。
//
//	原因は callee 名の解決が `SelectorExpr.Sel.Name` を返し、**レシーバを捨てて**
//	いたこと。型情報を持たない以上 `x.Set()` が何の型の Set かは決定不能なので、
//	**決定可能なもの(同一パッケージの裸の呼び出し)だけを報告する**。
//	package doc が元々「同一パッケージ内のコールにのみ効く」と主張していた挙動に、
//	実装をようやく合わせた形。
func TestScan_DoesNotFlagMethodCallsThatMerelyShareAName(t *testing.T) {
	files := map[string]string{
		"store.go": `package p

type Store struct{}

func (s *Store) Set(k string) error { return nil }
`,
		"user.go": `package p

import "net/http"

func handler(w http.ResponseWriter) {
	// http.Header.Set は戻り値を持たない——error を捨てようがない。
	w.Header().Set("Content-Type", "application/json")
}
`,
	}
	rep := errdiscard.Scan(files)
	for _, f := range rep.Findings {
		if f.File == "user.go" {
			t.Errorf("flagged %s:%d (%s) — a method call that merely shares the name %q "+
				"with an error-returning function proves nothing without type information",
				f.File, f.Line, f.Caller, f.Callee)
		}
	}
}

// 一方、**同一パッケージの裸の呼び出し** は今も検出できること(recall を全部
// 捨てたわけではない)。
func TestScan_StillFlagsBarePackageLocalCalls(t *testing.T) {
	files := map[string]string{
		"a.go": `package p

func mayFail() error { return nil }

func caller() {
	mayFail()
}
`,
	}
	rep := errdiscard.Scan(files)
	if len(rep.Findings) != 1 {
		t.Fatalf("want exactly 1 finding for the bare call, got %d: %+v", len(rep.Findings), rep.Findings)
	}
	if rep.Findings[0].Callee != "mayFail" {
		t.Errorf("callee: want mayFail, got %s", rep.Findings[0].Callee)
	}
}
