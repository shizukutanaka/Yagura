// userconfig.go — custom rule loading from .yagura/aiverify.json (v0.36.0).
//
// 動機 (Roadmap #4):
//   デフォルト rule set はポートフォリオ横断の共通ルールを提供するが、
//   プロジェクト固有の禁止 API/パターン(例: 社内 SDK の危険 call、domain 固有
//   の不変条件)はここに書けない。`.yagura/aiverify.json` に user rule を置いて
//   `yagura ai-verify` / `yagura_ai_verify` MCP tool が自動マージする。
//
// JSON 形式:
//   {
//     "rules": [
//       { "id": "my-rule", "pattern": "dangerousCall\\(", "category": "external",
//         "risk": "HIGH", "message": "do not call dangerousCall" }
//     ],
//     "disable": ["billing-stripe-uncaught"]
//   }
//
// ADR-0001: stdlib のみ (encoding/json + regexp)。YAML は外部依存なので JSON。
package aiverify

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

// UserConfig は .yagura/aiverify.json のトップレベル。
type UserConfig struct {
	// Rules はデフォルト rule set に追加する project 固有ルール群。
	Rules []UserRule `json:"rules,omitempty"`
	// Disable はデフォルト rule set から除外したい組み込みルール ID 群。
	Disable []string `json:"disable,omitempty"`
}

// UserRule はユーザー定義の 1 ルール(JSON 入力用、compiled regex は持たない)。
type UserRule struct {
	ID        string   `json:"id"`
	Pattern   string   `json:"pattern"`
	Category  string   `json:"category"`          // aiverify.Category の string 表現
	Risk      string   `json:"risk"`              // aiverify.RiskLevel の string 表現
	Message   string   `json:"message"`
	Languages []string `json:"languages,omitempty"`
}

const maxUserRulePatternLen = 1000

// LoadUserConfig は path の JSON ファイルを読み込む。
// ファイルが存在しない場合はエラーを返す(呼出側が ErrNotExist を無視して
// 省略扱いにすることも可能)。
func LoadUserConfig(path string) (*UserConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("aiverify: read user config %s: %w", path, err)
	}
	var cfg UserConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("aiverify: parse user config %s: %w", path, err)
	}
	return &cfg, nil
}

// Apply はデフォルト rule set (または任意の base rules) に対して:
//  1. cfg.Disable に含まれるルールを除外する
//  2. cfg.Rules をコンパイルして末尾に追加する
//
// 戻り値は新しい slice。base は変更されない。
// コンパイルエラーまたは必須フィールド欠落時はエラーを返す。
func (cfg *UserConfig) Apply(base []Rule) ([]Rule, error) {
	// Disable set
	disableSet := make(map[string]bool, len(cfg.Disable))
	for _, id := range cfg.Disable {
		disableSet[id] = true
	}

	// Filter base
	out := make([]Rule, 0, len(base)+len(cfg.Rules))
	for _, r := range base {
		if !disableSet[r.ID] {
			out = append(out, r)
		}
	}

	// Compile and append user rules
	for i, u := range cfg.Rules {
		if strings.TrimSpace(u.ID) == "" {
			return nil, fmt.Errorf("aiverify: custom rule #%d: id is required", i)
		}
		if strings.TrimSpace(u.Pattern) == "" {
			return nil, fmt.Errorf("aiverify: custom rule %q: pattern is required", u.ID)
		}
		if len(u.Pattern) > maxUserRulePatternLen {
			return nil, fmt.Errorf("aiverify: custom rule %q: pattern too long (>%d)", u.ID, maxUserRulePatternLen)
		}
		re, err := regexp.Compile(u.Pattern)
		if err != nil {
			return nil, fmt.Errorf("aiverify: custom rule %q: invalid pattern: %w", u.ID, err)
		}
		// Accept any category/risk string (forward-compatible; unknown values
		// behave like their zero-value equivalents rather than rejecting).
		out = append(out, Rule{
			ID:        u.ID,
			Pattern:   re,
			Category:  Category(u.Category),
			Risk:      RiskLevel(u.Risk),
			Message:   u.Message,
			Languages: u.Languages,
		})
	}
	return out, nil
}
