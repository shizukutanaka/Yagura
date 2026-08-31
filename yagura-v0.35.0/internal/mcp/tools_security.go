// tools_security.go: extracted from tools.go in v0.29 (gstack-style topic split).
//
// All tools in this file are registered via RegisterDefaultTools in tools.go.
// See CLAUDE.md Workflows for the registration pattern.

package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/shizukutanaka/yagura/internal/osv"
	"github.com/shizukutanaka/yagura/internal/project"
	"github.com/shizukutanaka/yagura/internal/scorecard"
	"strings"
	"time"
)

// vulnQuery は yagura_vulns の解決済みクエリ対象(package/ecosystem/version)。
type vulnQuery struct {
	pkg, ecosystem, version, resolvedFrom string
}

// resolveVulnQuery は slug 経由(registry → language → ecosystem 推定)または
// package+ecosystem 直接指定の 2 パターンを解決し、必須項目を検証する。
func resolveVulnQuery(d Deps, slug, pkg, ecosystem, version string) (vulnQuery, *ToolError) {
	q := vulnQuery{
		pkg:       strings.TrimSpace(pkg),
		ecosystem: strings.TrimSpace(ecosystem),
		version:   strings.TrimSpace(version),
	}
	if slug != "" {
		p, err := d.Registry.Get(slug)
		if err != nil {
			return q, &ToolError{Code: "not_found", Message: "project not found: " + slug}
		}
		if q.ecosystem == "" {
			q.ecosystem = osv.LanguageToEcosystem(p.Language)
		}
		if q.pkg == "" {
			// Repository field を package 識別子の fallback として使用
			// (Go の場合: "github.com/owner/repo" がそのまま module path)
			q.pkg = p.Repository
		}
		if q.version == "" && p.LatestVersion != "" {
			q.version = p.LatestVersion
		}
		q.resolvedFrom = slug
	}
	if q.pkg == "" {
		return q, &ToolError{Code: "invalid_input",
			Message: "package is required (either via slug or directly)"}
	}
	if q.ecosystem == "" {
		return q, &ToolError{Code: "invalid_input",
			Message: "ecosystem is required (could not infer from project language)"}
	}
	return q, nil
}

// filterVulnsBySeverity は min_severity 以上のみ残す(空なら全件)。
func filterVulnsBySeverity(vulns []osv.Vuln, minSeverity string) ([]osv.Vuln, *ToolError) {
	if minSeverity == "" {
		return vulns, nil
	}
	minRank := osv.SeverityRank(osv.Severity(strings.ToUpper(minSeverity)))
	if minRank == 0 && strings.ToUpper(minSeverity) != "UNKNOWN" {
		return nil, &ToolError{Code: "invalid_input",
			Message: "min_severity must be one of LOW/MEDIUM/HIGH/CRITICAL"}
	}
	filtered := vulns[:0]
	for _, v := range vulns {
		if osv.SeverityRank(v.Severity) >= minRank {
			filtered = append(filtered, v)
		}
	}
	return filtered, nil
}

func buildVulnsTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_vulns",
		Title:       "Query Package Vulnerabilities",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: true},
		Description: "[S] OSV.dev vulns by CVSS desc.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":      map[string]any{"type": "string", "description": "登録済みプロジェクトの slug"},
				"package":   map[string]any{"type": "string", "description": "パッケージ識別子(Go module path 等)"},
				"version":   map[string]any{"type": "string", "description": "バージョン文字列(省略時は全バージョン)"},
				"ecosystem": map[string]any{"type": "string", "description": "OSV ecosystem(Go/PyPI/npm 等)"},
				"min_severity": map[string]any{
					"type": "string",
				},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.OSV == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "OSV client not configured at startup"}
			}
			var in struct {
				Slug        string `json:"slug"`
				Package     string `json:"package"`
				Version     string `json:"version"`
				Ecosystem   string `json:"ecosystem"`
				MinSeverity string `json:"min_severity"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: "invalid args", Cause: err}
			}

			q, terr := resolveVulnQuery(d, in.Slug, in.Package, in.Ecosystem, in.Version)
			if terr != nil {
				return nil, terr
			}

			vulns, err := d.OSV.Query(ctx, q.ecosystem, q.pkg, q.version)
			if err != nil {
				return nil, &ToolError{Code: "osv_query_failed",
					Message: "OSV query failed", Cause: err}
			}

			vulns, terr = filterVulnsBySeverity(vulns, in.MinSeverity)
			if terr != nil {
				return nil, terr
			}

			// Count by severity for the summary
			countBy := map[string]int{}
			for _, v := range vulns {
				countBy[string(v.Severity)]++
			}

			return map[string]any{
				"package":       q.pkg,
				"ecosystem":     q.ecosystem,
				"version":       q.version,
				"resolved_from": q.resolvedFrom,
				"total":         len(vulns),
				"by_severity":   countBy,
				"vulns":         vulns,
				"queried_at":    d.Now().Format(time.RFC3339),
			}, nil
		},
	}
}

// resolveScorecardRepo は slug 経由(registry.Repository)または直接指定の repo を
// 解決して検証する。
func resolveScorecardRepo(d Deps, slug, repo string) (string, string, *ToolError) {
	repo = strings.TrimSpace(repo)
	var resolvedFrom string
	if slug != "" {
		p, err := d.Registry.Get(slug)
		if err != nil {
			return "", "", &ToolError{Code: "not_found", Message: "project not found: " + slug}
		}
		if repo == "" {
			repo = p.Repository
		}
		resolvedFrom = slug
	}
	if repo == "" {
		return "", "", &ToolError{Code: "invalid_input",
			Message: "repo is required (either via slug or directly)"}
	}
	return repo, resolvedFrom, nil
}

// filterPriorityChecks は重要 7 check のみ残す。
func filterPriorityChecks(checks []scorecard.Check) []scorecard.Check {
	priority := make(map[string]bool, 7)
	for _, name := range scorecard.PriorityChecks() {
		priority[name] = true
	}
	filtered := checks[:0]
	for _, c := range checks {
		if priority[c.Name] {
			filtered = append(filtered, c)
		}
	}
	return filtered
}

func buildScorecardTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_scorecard",
		Title:       "Fetch OpenSSF Scorecard",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: true},
		Description: "[S] OpenSSF Scorecard fetch. priority_only=top 7 checks.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":          map[string]any{"type": "string"},
				"repo":          map[string]any{"type": "string", "description": "owner/repo 形式"},
				"priority_only": map[string]any{"type": "boolean", "description": "重要 7 check に絞る"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			if d.Scorecard == nil {
				return nil, &ToolError{Code: "unavailable",
					Message: "scorecard client not configured at startup"}
			}
			var in struct {
				Slug         string `json:"slug"`
				Repo         string `json:"repo"`
				PriorityOnly bool   `json:"priority_only"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: "invalid args", Cause: err}
			}

			repo, resolvedFrom, terr := resolveScorecardRepo(d, in.Slug, in.Repo)
			if terr != nil {
				return nil, terr
			}

			score, err := d.Scorecard.Fetch(ctx, repo)
			if err != nil {
				if errors.Is(err, scorecard.ErrNotScored) {
					return nil, &ToolError{Code: "not_scored",
						Message: "Scorecard has not yet analyzed this repo. " +
							"Run scorecard.yml workflow once to bootstrap."}
				}
				return nil, &ToolError{Code: "scorecard_fetch_failed",
					Message: "scorecard API call failed", Cause: err}
			}

			checks := score.Checks
			if in.PriorityOnly {
				checks = filterPriorityChecks(checks)
			}

			return map[string]any{
				"repo":              score.Repo,
				"commit":            score.Commit,
				"score":             score.Score,
				"category":          scorecard.HealthCategory(score.Score),
				"analyzed_at":       score.Date.Format("2006-01-02"),
				"scorecard_version": score.ScorecardVersion,
				"resolved_from":     resolvedFrom,
				"checks":            checks,
				"check_count":       len(checks),
			}, nil
		},
	}
}

func buildHealthTool(d Deps) *Tool {
	return &Tool{
		Name:        "yagura_health",
		Title:       "Security Health Summary",
		Annotations: &ToolAnnotations{ReadOnlyHint: true, DestructiveHint: false, IdempotentHint: true, OpenWorldHint: false},
		Description: "[S] Security summary: vuln + Scorecard issues. Cached.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":       map[string]any{"type": "string"},
				"individual": map[string]any{"type": "boolean"},
			},
		},
		Handler: func(ctx context.Context, args json.RawMessage) (any, error) {
			var in struct {
				Slug       string `json:"slug"`
				Individual bool   `json:"individual"`
			}
			if err := json.Unmarshal(args, &in); err != nil {
				return nil, &ToolError{Code: "invalid_input", Message: "invalid args", Cause: err}
			}

			// 個別モード
			if in.Individual {
				if in.Slug == "" {
					return nil, &ToolError{Code: "invalid_input",
						Message: "slug is required when individual=true"}
				}
				p, err := d.Registry.Get(in.Slug)
				if err != nil {
					return nil, &ToolError{Code: "not_found",
						Message: "project not found: " + in.Slug}
				}
				return projectHealthSummary(p), nil
			}

			// 全体モード
			projects := d.Registry.List()
			agg := aggregatePortfolioHealth(projects)

			var avgScore float64
			if agg.totalScored > 0 {
				avgScore = agg.scoreSum / float64(agg.totalScored)
			}

			return map[string]any{
				"as_of":             d.Now().Format(time.RFC3339),
				"total_active":      len(projects), // archived 除く参考値
				"scorecard_scanned": agg.totalScored,
				"vulns_scanned":     agg.totalScannedForVulns,
				"not_yet_scanned":   agg.notYetScanned,
				"avg_scorecard":     avgScore,
				"total_vulns": map[string]int{
					"critical": agg.totalCritical,
					"high":     agg.totalHigh,
					"medium":   agg.totalMedium,
					"low":      agg.totalLow,
				},
				"needs_attention":       agg.needsAttention,
				"needs_attention_count": len(agg.needsAttention),
			}, nil
		},
	}
}

// portfolioHealth は yagura_health の全体モード集計値。
type portfolioHealth struct {
	totalScored, totalScannedForVulns int
	scoreSum                          float64
	totalCritical, totalHigh          int
	totalMedium, totalLow             int
	notYetScanned                     int
	// **空でも `[]`**(v1.3.3)。null は「注意の要る project 無し」と
	// 「集計していない」を区別できない。初期化は build 関数側で行う。
	needsAttention []map[string]any
}

// aggregatePortfolioHealth は active project 群の sensor field を 1 つの集計へ畳む。
func aggregatePortfolioHealth(projects []*project.Project) portfolioHealth {
	// 空でも `[]` を返す(v1.3.3)。null は「注意の要る project 無し」と
	// 「集計していない」を区別できない。
	agg := portfolioHealth{needsAttention: []map[string]any{}}
	for _, p := range projects {
		if p.Stage == project.StageArchived {
			continue
		}
		if !p.ScorecardAt.IsZero() {
			agg.totalScored++
			agg.scoreSum += p.ScorecardScore
		}
		if !p.VulnScanAt.IsZero() {
			agg.totalScannedForVulns++
		}
		agg.totalCritical += p.VulnCritical
		agg.totalHigh += p.VulnHigh
		agg.totalMedium += p.VulnMedium
		agg.totalLow += p.VulnLow
		if p.ScorecardAt.IsZero() && p.VulnScanAt.IsZero() {
			agg.notYetScanned++
		}
		if p.HasCriticalSecurityIssue() {
			agg.needsAttention = append(agg.needsAttention, map[string]any{
				"slug":            p.Slug,
				"repository":      p.Repository,
				"scorecard_score": p.ScorecardScore,
				"vuln_critical":   p.VulnCritical,
				"vuln_high":       p.VulnHigh,
			})
		}
	}
	return agg
}

func projectHealthSummary(p *project.Project) map[string]any {
	scoreCat := ""
	if !p.ScorecardAt.IsZero() {
		switch {
		case p.ScorecardScore >= 8.0:
			scoreCat = "excellent"
		case p.ScorecardScore >= 6.0:
			scoreCat = "good"
		case p.ScorecardScore >= 4.0:
			scoreCat = "fair"
		default:
			scoreCat = "poor"
		}
	}
	scorecardAt := ""
	if !p.ScorecardAt.IsZero() {
		scorecardAt = p.ScorecardAt.Format(time.RFC3339)
	}
	vulnScanAt := ""
	if !p.VulnScanAt.IsZero() {
		vulnScanAt = p.VulnScanAt.Format(time.RFC3339)
	}
	return map[string]any{
		"slug":               p.Slug,
		"repository":         p.Repository,
		"scorecard_score":    p.ScorecardScore,
		"scorecard_category": scoreCat,
		"scorecard_at":       scorecardAt,
		"vuln_critical":      p.VulnCritical,
		"vuln_high":          p.VulnHigh,
		"vuln_medium":        p.VulnMedium,
		"vuln_low":           p.VulnLow,
		"vuln_total":         p.TotalVulns(),
		"vuln_scan_at":       vulnScanAt,
		"needs_attention":    p.HasCriticalSecurityIssue(),
	}
}
