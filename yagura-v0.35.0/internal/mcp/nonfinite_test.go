package mcp

import (
	"context"
	"encoding/json"
	"math"
	"strings"
	"testing"
)

// TestFindNonFinite_NamesTheOffendingPath は「JSON にできない浮動小数点」を
// **場所つき** で報告することを要求する。
//
// 発見の経緯: v1.83.0 で `yagura_change_coupling` の lift が +Inf になり、
// encoding/json が構造体ごと落ちてツールの応答が丸ごと消えた。seam には
// エラー処理が在ったが、返るのは `json: unsupported value: +Inf` だけで
// **どのフィールドか言わない**。v1.86.0 で「効かない対処を指す診断は
// 診断していないより悪い」と書いた直後に、同じ欠陥が自分の seam に在った。
func TestFindNonFinite_NamesTheOffendingPath(t *testing.T) {
	type eval struct {
		Name string   `json:"name"`
		Lift float64  `json:"lift"`
		Rate *float64 `json:"rate"`
	}
	nan := math.NaN()
	v := map[string]any{
		"slug": "demo",
		"validation": map[string]any{
			"by_confidence": eval{Name: "conf", Lift: math.Inf(1)},
			"by_lift":       eval{Name: "lift", Lift: 1.5, Rate: &nan},
		},
		"series": []any{1.0, math.Inf(-1)},
	}
	got := findNonFinite(v)
	if len(got) == 0 {
		t.Fatal("expected the non-finite values to be located, got none")
	}
	joined := strings.Join(got, " | ")
	for _, want := range []string{
		"validation.by_confidence.lift",
		"validation.by_lift.rate",
		"series[1]",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("path %q not reported; got %s", want, joined)
		}
	}
}

// 健全な結果では何も報告しない——常に何か言う検査器は何も検査しない。
func TestFindNonFinite_SilentOnEncodableResults(t *testing.T) {
	v := map[string]any{
		"a": 1.0, "b": []any{1, 2, 3}, "c": map[string]any{"d": "x"},
		"e": nil, "f": []float64{0.5, -0.5},
	}
	if got := findNonFinite(v); len(got) != 0 {
		t.Errorf("encodable result must produce no findings, got %v", got)
	}
}

// seam を通した end-to-end 確認。unit で緑でも live で再発した v1.3.3 の
// null コレクション事件があるので、実際に tool を登録して呼ぶところまで見る。
func TestToolsCall_NonFiniteResultNamesTheField(t *testing.T) {
	s, _ := newServerForTest(t, "")
	s.Register(&Tool{
		Name:        "yagura_test_nonfinite",
		Title:       "Non-finite",
		Description: "test",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true},
		InputSchema: map[string]any{"type": "object"},
		Handler: func(_ context.Context, _ json.RawMessage) (any, error) {
			return map[string]any{
				"validation": map[string]any{"lift": math.Inf(1)},
			}, nil
		},
	})

	body := `{"jsonrpc":"2.0","id":1,"method":"tools/call",` +
		`"params":{"name":"yagura_test_nonfinite","arguments":{}}}`
	rec := postJSON(t, s, body, "").Body.String()

	if !strings.Contains(rec, "validation.lift") {
		t.Errorf("the error must name the offending JSON path; got: %s", rec)
	}
	if !strings.Contains(rec, "null") {
		t.Errorf("the error must say what to do instead (encode as null); got: %s", rec)
	}
}
