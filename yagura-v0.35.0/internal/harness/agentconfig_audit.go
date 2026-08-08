// agentconfig_audit.go: OpenClaw 系 multi-provider AI エージェント設定(openclaw.json)
// の heuristic 評価。
//
// 動機: OS を直接触れる自律エージェント(OpenClaw 等)を「いかに安全に実務へ組み込むか」
// が論点で、設定 1 箇所のミスが数時間のデバッグを生む(LM Studio / vLLM / クラウド API
// の混在、Vision の input、compaction、LAN 公開の認証)。これらは構造ベースの lint rule
// になる。本 auditor は openclaw.json を strict JSON で parse し、security と reliability
// の foot-gun を検出する:
//
//	security:
//	  - gateway.controlUi.dangerouslyDisableDeviceAuth=true(HTTP/LAN 公開で control UI 露出)
//	  - browser.noSandbox=true(Chromium sandbox 無効 — privilege escalation 面)
//	  - providers[].apiKey が placeholder でない実 key の直書き(secret leak)
//	  - 弱い gateway token(例値・123・短すぎ)
//	reliability:
//	  - compaction.reserveTokensFloor < 50000(小さすぎると圧縮自体が失敗)
//	  - model.maxTokens >= contextWindow / contextWindow 未設定(overflow)
//	  - agents.defaults.model.primary / models のキーが providers の宣言モデルに解決しない
//
// AuditSkill / AuditSubagent / AuditWorkflow / AuditSettings と同じ shape
// (Score + Issues + Suggestions)。stdlib encoding/json のみ(ADR-0001)。
package harness

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// AgentConfigAuditResult は openclaw.json 評価結果。
//
// Score は 0-100:
//
//	90+ : security/reliability とも健全
//	70-89: 動くが LAN 公開 / sandbox 等のトレードオフあり
//	50-69: 複数 foot-gun(参照不整合 / context 設定ミス等)
//	<50 : 起動不能 or 重大な secret/security 露出
type AgentConfigAuditResult struct {
	Score              int      `json:"score"`
	ValidJSON          bool     `json:"valid_json"`
	ProviderCount      int      `json:"provider_count"`
	ModelCount         int      `json:"model_count"`
	PrimaryModel       string   `json:"primary_model,omitempty"`
	PrimaryResolves    bool     `json:"primary_resolves"`
	DeviceAuthDisabled bool     `json:"device_auth_disabled"`
	BrowserNoSandbox   bool     `json:"browser_no_sandbox"`
	HardcodedKeys      []string `json:"hardcoded_key_providers,omitempty"`
	CompactionSafe     bool     `json:"compaction_safe"`
	Issues             []string `json:"issues,omitempty"`
	Suggestions        []string `json:"suggestions,omitempty"`
}

// compactionFloorRecommended は記事が推奨する reserveTokensFloor の下限。
const compactionFloorRecommended = 50000

// apiKeyPlaceholders は「実 key ではない」とみなす値(local provider の EMPTY など)。
var apiKeyPlaceholders = map[string]bool{
	"": true, "EMPTY": true, "API-KEY": true, "APIKEY": true,
	"YOUR_API_KEY": true, "YOUR-API-KEY": true, "<API-KEY>": true,
	"CHANGEME": true, "DUMMY": true, "TODO": true, "XXX": true, "NONE": true,
}

// agentConfigDoc は openclaw.json の audit 対象フィールドのみ抽出する。
type agentConfigDoc struct {
	Gateway struct {
		Bind string `json:"bind"`
		Auth struct {
			Mode  string `json:"mode"`
			Token string `json:"token"`
		} `json:"auth"`
		ControlUI struct {
			AllowedOrigins               []string `json:"allowedOrigins"`
			DangerouslyDisableDeviceAuth bool     `json:"dangerouslyDisableDeviceAuth"`
		} `json:"controlUi"`
	} `json:"gateway"`
	Browser struct {
		Enabled   bool `json:"enabled"`
		NoSandbox bool `json:"noSandbox"`
	} `json:"browser"`
	Agents struct {
		Defaults struct {
			Model struct {
				Primary string `json:"primary"`
			} `json:"model"`
			Models     map[string]json.RawMessage `json:"models"`
			Compaction struct {
				Mode               string `json:"mode"`
				ReserveTokensFloor int    `json:"reserveTokensFloor"`
			} `json:"compaction"`
		} `json:"defaults"`
	} `json:"agents"`
	Models struct {
		Providers map[string]struct {
			BaseURL string `json:"baseUrl"`
			APIKey  string `json:"apiKey"`
			Models  []struct {
				ID            string   `json:"id"`
				Name          string   `json:"name"`
				Input         []string `json:"input"`
				ContextWindow int      `json:"contextWindow"`
				MaxTokens     int      `json:"maxTokens"`
			} `json:"models"`
		} `json:"providers"`
	} `json:"models"`
}

// AuditAgentConfig は openclaw.json content を heuristic で評価する。
func AuditAgentConfig(content string) AgentConfigAuditResult {
	r := AgentConfigAuditResult{Score: 100, CompactionSafe: true}

	var doc agentConfigDoc
	if err := json.Unmarshal([]byte(content), &doc); err != nil {
		r.ValidJSON = false
		r.Score = 0
		r.Issues = append(r.Issues, "invalid JSON — OpenClaw cannot load this config at all")
		r.Suggestions = append(r.Suggestions,
			"openclaw.json must be strict JSON (the article's template uses # comments for explanation only — delete them before use).")
		return r
	}
	r.ValidJSON = true

	validRefs := auditAgentModels(&r, &doc)
	auditAgentReferences(&r, &doc, validRefs)
	auditAgentCompaction(&r, &doc)
	auditAgentGateway(&r, &doc)

	if r.Score < 0 {
		r.Score = 0
	}
	return r
}

// auditAgentModels は provider / model を集計し、各 model の context 設定と
// provider の hardcoded apiKey を評価する。解決済み "provider/modelId" の集合を返す。
func auditAgentModels(r *AgentConfigAuditResult, doc *agentConfigDoc) map[string]bool {
	validRefs := map[string]bool{}
	for pname, prov := range doc.Models.Providers {
		r.ProviderCount++
		for _, m := range prov.Models {
			r.ModelCount++
			validRefs[pname+"/"+m.ID] = true
			auditAgentModelContext(r, pname, m)
		}
		// security: provider の apiKey 直書き。
		if isRealAPIKey(prov.APIKey) {
			r.HardcodedKeys = append(r.HardcodedKeys, pname)
		}
	}

	if r.ProviderCount == 0 {
		r.Score -= 20
		r.Issues = append(r.Issues, "no model providers declared (models.providers is empty)")
		r.Suggestions = append(r.Suggestions,
			"Declare at least one provider (lmstudio/vllm/cloud) with its baseUrl and models.")
	}

	// security: hardcoded API key。
	if len(r.HardcodedKeys) > 0 {
		sort.Strings(r.HardcodedKeys)
		r.Score -= 20
		r.Issues = append(r.Issues, "hardcoded API key(s) in config for provider(s): "+strings.Join(r.HardcodedKeys, ", "))
		r.Suggestions = append(r.Suggestions,
			"Keep secrets out of openclaw.json (local providers use \"EMPTY\"; for cloud, inject the key via env/secret store, not the committed config).")
	}
	return validRefs
}

// agentModel は 1 モデルの context 評価に必要なフィールドを取る型エイリアス。
// (agentConfigDoc 内の匿名 struct と同じ shape — auditAgentModelContext 用に切り出す。)
type agentModel = struct {
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Input         []string `json:"input"`
	ContextWindow int      `json:"contextWindow"`
	MaxTokens     int      `json:"maxTokens"`
}

// auditAgentModelContext は 1 モデルの contextWindow / maxTokens / input modality を評価する。
func auditAgentModelContext(r *AgentConfigAuditResult, pname string, m agentModel) {
	if m.ContextWindow <= 0 {
		r.Score -= 8
		r.Issues = append(r.Issues, fmt.Sprintf("model %q: contextWindow unset/zero — set it to the model's real context length (mismatch overflows fast)", refName(pname, m.ID)))
	} else if m.MaxTokens >= m.ContextWindow {
		r.Score -= 8
		r.Issues = append(r.Issues, fmt.Sprintf("model %q: maxTokens (%d) >= contextWindow (%d) — no room for input, will overflow", refName(pname, m.ID), m.MaxTokens, m.ContextWindow))
	}
	// input 値の健全性(text/image 以外は誤設定の可能性)。
	for _, in := range m.Input {
		switch strings.ToLower(strings.TrimSpace(in)) {
		case "text", "image", "audio", "video":
		default:
			r.Suggestions = append(r.Suggestions,
				fmt.Sprintf("model %q: unrecognized input modality %q (Vision needs input:[\"text\",\"image\"])", refName(pname, m.ID), in))
		}
	}
}

// auditAgentReferences は primary model と Control UI の models メニューの参照解決を評価する。
func auditAgentReferences(r *AgentConfigAuditResult, doc *agentConfigDoc, validRefs map[string]bool) {
	r.PrimaryModel = strings.TrimSpace(doc.Agents.Defaults.Model.Primary)
	if r.PrimaryModel == "" {
		r.Score -= 10
		r.Issues = append(r.Issues, "agents.defaults.model.primary is unset — no default model to run")
	} else if validRefs[r.PrimaryModel] {
		r.PrimaryResolves = true
	} else {
		r.Score -= 15
		r.Issues = append(r.Issues, fmt.Sprintf("primary model %q does not resolve to any declared provider model — the agent will fail to select it", r.PrimaryModel))
		r.Suggestions = append(r.Suggestions,
			"primary must be \"provider/modelId\" matching a models.providers[*].models[*].id exactly.")
	}
	// Control UI のモデル一覧キーも解決を要求。
	danglingMenu := make([]string, 0)
	for ref := range doc.Agents.Defaults.Models {
		if !validRefs[ref] {
			danglingMenu = append(danglingMenu, ref)
		}
	}
	if len(danglingMenu) > 0 {
		sort.Strings(danglingMenu)
		r.Score -= 5
		r.Issues = append(r.Issues, "agents.defaults.models lists unresolved model ref(s): "+strings.Join(danglingMenu, ", "))
	}
}

// auditAgentCompaction は compaction.reserveTokensFloor の健全性を評価する。
func auditAgentCompaction(r *AgentConfigAuditResult, doc *agentConfigDoc) {
	if doc.Agents.Defaults.Compaction.Mode == "" {
		return
	}
	floor := doc.Agents.Defaults.Compaction.ReserveTokensFloor
	if floor > 0 && floor < compactionFloorRecommended {
		r.CompactionSafe = false
		r.Score -= 10
		r.Issues = append(r.Issues, fmt.Sprintf("compaction.reserveTokensFloor (%d) is below the recommended %d — too small and compaction itself can fail", floor, compactionFloorRecommended))
	} else if floor == 0 {
		r.CompactionSafe = false
		r.Score -= 8
		r.Issues = append(r.Issues, fmt.Sprintf("compaction.mode is set but reserveTokensFloor is unset — set it to ~%d so compression has room", compactionFloorRecommended))
	}
}

// auditAgentGateway は gateway / browser の security foot-gun(device auth / sandbox / weak token)を評価する。
func auditAgentGateway(r *AgentConfigAuditResult, doc *agentConfigDoc) {
	r.DeviceAuthDisabled = doc.Gateway.ControlUI.DangerouslyDisableDeviceAuth
	if r.DeviceAuthDisabled {
		r.Score -= 15
		r.Issues = append(r.Issues, "gateway.controlUi.dangerouslyDisableDeviceAuth=true — device auth off; only acceptable behind HTTPS, otherwise the control UI is exposed over HTTP/LAN")
		r.Suggestions = append(r.Suggestions,
			"Prefer HTTPS and leave device auth on. If LAN HTTP is unavoidable, restrict allowedOrigins and the network.")
	}
	r.BrowserNoSandbox = doc.Browser.NoSandbox
	if r.BrowserNoSandbox {
		r.Score -= 10
		r.Issues = append(r.Issues, "browser.noSandbox=true — Chromium sandbox disabled; sometimes needed in containers but it is a privilege-escalation surface")
		r.Suggestions = append(r.Suggestions,
			"Keep the OpenClaw container itself isolated (Docker) so a disabled in-browser sandbox is not the only boundary.")
	}
	// 弱い gateway token。
	if doc.Gateway.Auth.Mode == "token" && isWeakToken(doc.Gateway.Auth.Token) {
		r.Score -= 5
		r.Issues = append(r.Issues, "gateway.auth.token looks weak/example (e.g. contains '123' / 'local-token' / too short)")
		r.Suggestions = append(r.Suggestions,
			"Use a long random token, especially when bind is 'lan'.")
	}
}

func refName(provider, id string) string { return provider + "/" + id }

// isRealAPIKey は apiKey が placeholder ではなく、実 secret らしいか。
func isRealAPIKey(k string) bool {
	t := strings.TrimSpace(k)
	if apiKeyPlaceholders[strings.ToUpper(t)] {
		return false
	}
	// 既知 prefix or 十分な長さなら実 key とみなす。
	for _, p := range []string{"sk-", "sk_", "ghp_", "gho_", "xai-", "aiza", "anthropic", "pk-"} {
		if strings.HasPrefix(strings.ToLower(t), p) {
			return true
		}
	}
	return len(t) >= 16
}

// isWeakToken は gateway token が弱い/例値か。
func isWeakToken(tok string) bool {
	t := strings.TrimSpace(tok)
	if t == "" {
		return false // 別チェック(token mode で空)は対象外、ここは「弱い値」を見る
	}
	low := strings.ToLower(t)
	for _, bad := range []string{"123", "changeme", "example", "local-token", "test", "secret", "password"} {
		if strings.Contains(low, bad) {
			return true
		}
	}
	return len(t) < 12
}
