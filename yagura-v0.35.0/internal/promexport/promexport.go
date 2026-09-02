// Package promexport は Prometheus exposition text format を zero-dep で
// 生成する (https://prometheus.io/docs/instrumenting/exposition_formats/)。
//
// Motivation (v0.31.0):
//
//	m の harness G16 が「OpenTelemetry(OTel)を標準プロトコルとして採用」
//	と明示し、Prometheus + Grafana も推奨。yagura は既に ToolStats / Cache
//	stats / AlertFix stats / HookReceiver stats を内部に持つが、export 経路
//	が無かった。
//
// 設計判断 (ADR-0001 ゼロ依存):
//   - prometheus/client_golang は外部 dep のため使えず
//   - Text format は dead simple: `# HELP name desc\n# TYPE name counter\nname{label="x"} 5\n`
//   - stdlib only (fmt + sort + io)
//   - Labels の order を sort して deterministic output
//   - Counter / Gauge のみ実装 (Histogram / Summary は yagura に不要)
//
// 性能:
//   - 39 MCP tools × 6 fields = ~250 metrics 出力、~10 KB レスポンス、<1ms
package promexport

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// Metric は 1 つの metric 出力 (1 line in Prometheus exposition)。
type Metric struct {
	Name   string            // yagura_tool_calls_total
	Type   string            // "counter" | "gauge"
	Help   string            // 1-line description
	Labels map[string]string // tool="x", project="y"
	Value  float64
}

// Collection は同一 (Name, Type, Help) を共有する series 集合。
type Collection struct {
	Name    string
	Type    string
	Help    string
	Samples []Sample
}

// Sample は 1 series の (labels, value) pair。
type Sample struct {
	Labels map[string]string
	Value  float64
}

// Render は collection 配列を Prometheus text format で書き出す。
//
// 同じ (Name, Type, Help) の HELP/TYPE は 1 度のみ出力される(spec 要件)。
// label と sample は deterministic sort される。
func Render(w io.Writer, cs []Collection) error {
	// Name で sort して deterministic 出力
	sort.Slice(cs, func(i, j int) bool { return cs[i].Name < cs[j].Name })
	for _, c := range cs {
		if c.Help != "" {
			if _, err := fmt.Fprintf(w, "# HELP %s %s\n", c.Name, escapeHelp(c.Help)); err != nil {
				return err
			}
		}
		if c.Type != "" {
			if _, err := fmt.Fprintf(w, "# TYPE %s %s\n", c.Name, c.Type); err != nil {
				return err
			}
		}
		// samples を deterministic 順序で
		sort.Slice(c.Samples, func(i, j int) bool {
			return labelsKey(c.Samples[i].Labels) < labelsKey(c.Samples[j].Labels)
		})
		for _, s := range c.Samples {
			line := c.Name
			if len(s.Labels) > 0 {
				line += "{" + renderLabels(s.Labels) + "}"
			}
			line += fmt.Sprintf(" %s\n", formatFloat(s.Value))
			if _, err := io.WriteString(w, line); err != nil {
				return err
			}
		}
	}
	return nil
}

// renderLabels は labels を deterministic key=value, comma-separated に。
//
// 値は spec 通り escape: \\ \" \n
func renderLabels(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf(`%s="%s"`, k, escapeLabelValue(labels[k])))
	}
	return strings.Join(parts, ",")
}

func labelsKey(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(labels[k])
		sb.WriteByte(',')
	}
	return sb.String()
}

// escapeLabelValue は label value を Prometheus spec に従い escape する。
//
// spec: backslash, double-quote, newline must be escaped.
func escapeLabelValue(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}

// escapeHelp は HELP value を spec に従い escape (backslash + newline のみ)。
func escapeHelp(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, "\n", `\n`)
	return v
}

// formatFloat は Prometheus が受け付ける float 表記。
//
// 整数なら "5"、小数なら "1.5"、Inf / NaN は spec 文字列。
func formatFloat(v float64) string {
	if v != v {
		return "NaN"
	}
	if v > 1e18 {
		return "+Inf"
	}
	if v < -1e18 {
		return "-Inf"
	}
	if v == float64(int64(v)) {
		return fmt.Sprintf("%d", int64(v))
	}
	return fmt.Sprintf("%g", v)
}
