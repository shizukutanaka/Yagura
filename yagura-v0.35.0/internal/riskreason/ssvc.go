package riskreason

import "strings"

// SSVC は CISA SSVC(Stakeholder-Specific Vulnerability Categorization)整合の
// 決定木出力。SSVC は決定論的決定木そのものなので Yagura の rule-based 思想に完全に
// 適合する。Yagura が持つシグナルを SSVC の deployer 決定点(Exploitation / Exposure /
// Automatable / Mission&Well-being Impact)へマップし、Act/Attend/Track*/Track を返す。
// 各決定点の値も返すので判断が監査可能(独自スコアと違い業界標準語彙)。
//
// 参考: CISA SSVC, arXiv 2508.13644(scoring system 比較), 2506.01220(CVSS+EPSS+KEV chaining)。
// 注: CISA の完全な lookup table の bit-exact 再現ではなく、Yagura の利用可能シグナルへの
// 透明なマッピング + SSVC の意図に沿った決定。決定点を出力するので解釈は追跡可能。
type SSVC struct {
	Priority     SSVCPriority `json:"priority"`
	Exploitation string       `json:"exploitation"` // none / poc / active
	Exposure     string       `json:"exposure"`     // small / controlled / open
	Automatable  string       `json:"automatable"`  // no / yes
	Impact       string       `json:"impact"`       // low / medium / high
}

// SSVCPriority は SSVC の deployer outcome。
type SSVCPriority string

const (
	// SSVCAct は即対応すべき outcome。
	SSVCAct SSVCPriority = "Act"
	// SSVCAttend は通常より早く対応すべき outcome。
	SSVCAttend SSVCPriority = "Attend"
	// SSVCTrackStar は注意して追跡すべき outcome。
	SSVCTrackStar SSVCPriority = "Track*"
	// SSVCTrack は通常サイクルで追跡する outcome。
	SSVCTrack SSVCPriority = "Track"
)

// evalSSVC は Input を SSVC 決定点へマップし、決定木を適用する。決定論的。
func evalSSVC(in Input) SSVC {
	s := SSVC{
		Exploitation: ssvcExploitation(in),
		Exposure:     ssvcExposure(in),
		Automatable:  ssvcAutomatable(in),
		Impact:       ssvcImpact(in),
	}
	s.Priority = ssvcDecide(s.Exploitation, s.Exposure, s.Automatable, s.Impact)
	return s
}

func ssvcExploitation(in Input) string {
	if isTrue(in.KnownExploited) {
		return "active"
	}
	if isTrue(in.PublicExploit) {
		return "poc"
	}
	return "none"
}

func ssvcExposure(in Input) string {
	if in.InternetExposed == nil {
		return "controlled" // 不明は中間
	}
	if *in.InternetExposed {
		return "open"
	}
	return "small"
}

func ssvcAutomatable(in Input) string {
	// 明示があれば従う。無ければ高 EPSS を automatable の代理シグナルとみなす。
	if in.Automatable != nil {
		if *in.Automatable {
			return "yes"
		}
		return "no"
	}
	if in.EPSS >= 0.5 {
		return "yes"
	}
	return "no"
}

func ssvcImpact(in Input) string {
	sensitive, production := false, false
	for _, t := range in.Tags {
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "pii", "secret", "financial", "payment", "audit", "confidential":
			sensitive = true
		case "production", "prod", "external", "public", "internet-facing":
			production = true
		}
	}
	if in.AssetPriority >= 4 || sensitive {
		return "high"
	}
	if in.AssetPriority >= 2 || production {
		return "medium"
	}
	return "low"
}

// ssvcDecide は SSVC 決定点 → outcome(CISA deployer tree の意図に沿った決定)。
func ssvcDecide(exploitation, exposure, automatable, impact string) SSVCPriority {
	high := impact == "high"
	open := exposure == "open"
	auto := automatable == "yes"

	switch exploitation {
	case "active":
		if high || open || auto {
			return SSVCAct
		}
		return SSVCAttend
	case "poc":
		switch {
		case high && open:
			return SSVCAct
		case high || open || auto:
			return SSVCAttend
		case impact == "medium" || exposure == "controlled":
			return SSVCTrackStar
		default:
			return SSVCTrack
		}
	default: // none
		switch {
		case high && open:
			return SSVCAttend
		case high || open:
			return SSVCTrackStar
		default:
			return SSVCTrack
		}
	}
}
