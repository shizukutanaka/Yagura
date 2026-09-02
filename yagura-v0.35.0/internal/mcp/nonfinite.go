package mcp

import (
	"fmt"
	"math"
	"reflect"
	"sort"
	"strings"
)

// maxNonFiniteReported は 1 応答あたりに名指しする件数の上限。
// 全部並べると診断自体が読めなくなるので、代表を出して総数を添える。
const maxNonFiniteReported = 10

// findNonFinite は v の中の +Inf / -Inf / NaN を **JSON パスつき** で列挙する。
//
// なぜ要るか:
//
//	+Inf / NaN は JSON の数値ではないので encoding/json は **構造体ごと**
//	marshal に失敗する。1 フィールドが欠けるのではなく、tool の応答が丸ごと
//	返らなくなる(v1.83.0 に `yagura_change_coupling` で実際に起きた)。
//	seam にエラー処理は在ったが、返るのは `json: unsupported value: +Inf` だけで
//	**どこが原因か言わない**。
//
//	v1.86.0 で「効かない対処を指す診断は、診断していないより悪い」と書いた
//	(git log の timeout が partial clone を「履歴が大きすぎる」と誤診していた件)。
//	その直後に、同じ欠陥が自分の MCP seam に在った——直せない診断ではなく、
//	**直す場所を名指ししない診断**という形で。
//
// json タグを尊重するので、報告されるパスは利用者が実際に受け取る JSON の
// 形と一致する(Go のフィールド名ではなく)。
func findNonFinite(v any) []string {
	var out []string
	seen := map[uintptr]bool{}
	walkNonFinite(reflect.ValueOf(v), "", &out, seen)
	sort.Strings(out)
	return out
}

func walkNonFinite(rv reflect.Value, path string, out *[]string, seen map[uintptr]bool) {
	if len(*out) >= maxNonFiniteReported {
		return
	}
	if !rv.IsValid() {
		return
	}
	switch rv.Kind() {
	case reflect.Interface, reflect.Pointer:
		if rv.IsNil() {
			return
		}
		// 循環参照で無限再帰しない(any の入れ子は自由なので起こりうる)。
		if rv.Kind() == reflect.Pointer {
			if p := rv.Pointer(); seen[p] {
				return
			} else {
				seen[p] = true
			}
		}
		walkNonFinite(rv.Elem(), path, out, seen)
	case reflect.Float32, reflect.Float64:
		f := rv.Float()
		if math.IsInf(f, 0) || math.IsNaN(f) {
			*out = append(*out, fmt.Sprintf("%s (%v)", nonEmptyPath(path), f))
		}
	case reflect.Slice, reflect.Array:
		if rv.Kind() == reflect.Slice && rv.IsNil() {
			return
		}
		for i := 0; i < rv.Len(); i++ {
			walkNonFinite(rv.Index(i), fmt.Sprintf("%s[%d]", path, i), out, seen)
		}
	case reflect.Map:
		if rv.IsNil() {
			return
		}
		keys := rv.MapKeys()
		sort.Slice(keys, func(a, b int) bool {
			return fmt.Sprint(keys[a].Interface()) < fmt.Sprint(keys[b].Interface())
		})
		for _, k := range keys {
			walkNonFinite(rv.MapIndex(k), join(path, fmt.Sprint(k.Interface())), out, seen)
		}
	case reflect.Struct:
		t := rv.Type()
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if f.PkgPath != "" { // unexported: JSON に出ないので原因になりえない
				continue
			}
			name := jsonFieldName(f.Tag.Get("json"), f.Name)
			if name == "" { // json:"-"
				continue
			}
			walkNonFinite(rv.Field(i), join(path, name), out, seen)
		}
	}
}

// jsonFieldName は struct tag から JSON 上の名前を取り出す。
// 報告するパスは利用者が受け取る JSON と一致していなければ意味がない。
func jsonFieldName(tag, fallback string) string {
	if tag == "" {
		return fallback
	}
	name, _, _ := strings.Cut(tag, ",")
	switch name {
	case "-":
		return ""
	case "":
		return fallback
	}
	return name
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func nonEmptyPath(p string) string {
	if p == "" {
		return "(root)"
	}
	return p
}

// describeNonFinite は marshal 失敗時に添える人間向けの説明を作る。
// 場所が特定できなければ空文字を返す(推測で語らない)。
func describeNonFinite(v any) string {
	paths := findNonFinite(v)
	if len(paths) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("non-finite float(s) cannot be encoded as JSON, so the whole result was " +
		"dropped rather than one field: ")
	b.WriteString(strings.Join(paths, ", "))
	if len(paths) >= maxNonFiniteReported {
		b.WriteString(", … (truncated)")
	}
	b.WriteString(". A ratio with a zero denominator is UNDEFINED: encode it as null " +
		"(*float64 nil) and say why in the note — do not substitute a large finite value, " +
		"and do not leave 0, which reads as 'worse than random'.")
	return b.String()
}
