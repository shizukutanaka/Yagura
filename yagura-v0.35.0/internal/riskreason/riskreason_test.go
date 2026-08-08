package riskreason

import (
	"strings"
	"testing"
)

func b(v bool) *bool { return &v }

func hasFactor(r Result, name string) bool {
	for _, f := range r.Factors {
		if f.Name == name {
			return true
		}
	}
	return false
}

func TestScore_CriticalExposedExploited_IsNow(t *testing.T) {
	r := Score(Input{
		CVE: "CVE-2026-0001", CVSS: 9.8, AssetPriority: 5,
		Tags:            []string{"production", "pii"},
		InternetExposed: b(true), AuthRequired: b(false),
		KnownExploited: b(true), PublicExploit: b(true), Dependents: 4,
	})
	if r.Priority != PriorityNow {
		t.Errorf("expected NOW, got %s (score %d)", r.Priority, r.Score)
	}
	for _, name := range []string{"severity", "asset_priority", "reachability", "known_exploited", "blast_radius"} {
		if !hasFactor(r, name) {
			t.Errorf("expected factor %q in rationale, got %+v", name, r.Factors)
		}
	}
	if !strings.Contains(r.Recommendation, "Actively exploited") {
		t.Errorf("recommendation should flag active exploitation: %q", r.Recommendation)
	}
}

func TestScore_BusinessContextLowersPriority(t *testing.T) {
	// Same critical CVE, but on an archived, internal, non-exposed asset → not urgent.
	r := Score(Input{
		CVE: "CVE-2026-0002", CVSS: 9.8, AssetPriority: 1,
		Stage: "archived", Tags: []string{"internal", "dev"},
		InternetExposed: b(false), KnownExploited: b(false), PublicExploit: b(false),
	})
	if r.Priority == PriorityNow {
		t.Errorf("archived internal asset should not be NOW despite critical CVSS: score %d", r.Score)
	}
	if !hasFactor(r, "stage") {
		t.Errorf("expected an archived stage factor lowering score: %+v", r.Factors)
	}
}

func TestScore_UnknownsListed(t *testing.T) {
	// Only CVE + CVSS — every reachability/exploit factor is unknown.
	r := Score(Input{CVE: "CVE-2026-0003", CVSS: 7.5})
	want := []string{"internet exposure", "authentication requirement", "WAF coverage",
		"known-exploited status (CISA KEV)", "public exploit availability"}
	for _, w := range want {
		found := false
		for _, u := range r.Unknowns {
			if u == w {
				found = true
			}
		}
		if !found {
			t.Errorf("expected unknown %q, got %+v", w, r.Unknowns)
		}
	}
	if !strings.Contains(r.Recommendation, "unknown") {
		t.Errorf("recommendation should mention context gaps: %q", r.Recommendation)
	}
}

func TestScore_PatchImpactChangesResponseNotScore(t *testing.T) {
	base := Input{CVE: "CVE-2026-0004", CVSS: 9.5, AssetPriority: 5,
		InternetExposed: b(true), KnownExploited: b(true)}
	a := Score(base)
	withImpact := base
	withImpact.PatchBlocksBusiness = b(true)
	c := Score(withImpact)
	if a.Score != c.Score {
		t.Errorf("patch business impact must not change the risk score: %d vs %d", a.Score, c.Score)
	}
	if a.Recommendation == c.Recommendation {
		t.Error("patch business impact should change the recommendation")
	}
	if !strings.Contains(c.Recommendation, "compensating controls") {
		t.Errorf("blocked-patch NOW should suggest compensating controls: %q", c.Recommendation)
	}
}

func TestScore_Deterministic(t *testing.T) {
	in := Input{CVE: "CVE-2026-0005", CVSS: 8.1, AssetPriority: 3, Tags: []string{"production"},
		InternetExposed: b(true), Dependents: 2}
	if Score(in).Score != Score(in).Score {
		t.Error("scoring must be deterministic")
	}
}

func TestScoreAll_OrderedByScoreDesc(t *testing.T) {
	rs := ScoreAll([]Input{
		{CVE: "CVE-low", CVSS: 3.0, Stage: "archived"},
		{CVE: "CVE-hi", CVSS: 9.8, AssetPriority: 5, InternetExposed: b(true), KnownExploited: b(true)},
		{CVE: "CVE-mid", CVSS: 6.0, AssetPriority: 2},
	})
	if rs[0].CVE != "CVE-hi" || rs[len(rs)-1].CVE != "CVE-low" {
		t.Errorf("expected score-desc order hi..low, got %s..%s", rs[0].CVE, rs[len(rs)-1].CVE)
	}
}

func TestScore_SeverityFromStringWhenNoCVSS(t *testing.T) {
	r := Score(Input{CVE: "CVE-2026-0006", Severity: "high", AssetPriority: 2})
	if !hasFactor(r, "severity") {
		t.Errorf("severity string should drive the base factor: %+v", r.Factors)
	}
}

func TestScore_TagSignals(t *testing.T) {
	prod := Score(Input{CVE: "a", CVSS: 5, Tags: []string{"production", "pii"}})
	dev := Score(Input{CVE: "b", CVSS: 5, Tags: []string{"dev"}})
	if prod.Score <= dev.Score {
		t.Errorf("production+pii should outrank a dev asset: prod=%d dev=%d", prod.Score, dev.Score)
	}
}

func TestScore_PatchImpactUnknownSurfaced(t *testing.T) {
	// nil patch-impact must surface as an unknown (consistent with other *bool gaps).
	nilCase := Score(Input{CVE: "a", CVSS: 9.0, InternetExposed: b(true), AuthRequired: b(true),
		WAFProtected: b(true), KnownExploited: b(true), PublicExploit: b(true)})
	found := false
	for _, u := range nilCase.Unknowns {
		if u == "patch business impact" {
			found = true
		}
	}
	if !found {
		t.Errorf("nil PatchBlocksBusiness should be an unknown, got %+v", nilCase.Unknowns)
	}
	// when set, it must NOT be listed as unknown.
	setCase := Score(Input{CVE: "a", CVSS: 9.0, InternetExposed: b(true), AuthRequired: b(true),
		WAFProtected: b(true), KnownExploited: b(true), PublicExploit: b(true), PatchBlocksBusiness: b(false)})
	for _, u := range setCase.Unknowns {
		if u == "patch business impact" {
			t.Error("a set PatchBlocksBusiness must not be an unknown")
		}
	}
}

func TestScoreWith_DefaultMatchesScore(t *testing.T) {
	in := Input{CVE: "a", CVSS: 8.1, AssetPriority: 3, Tags: []string{"production"},
		InternetExposed: b(true), KnownExploited: b(true), Dependents: 2}
	if Score(in).Score != ScoreWith(in, DefaultWeights()).Score {
		t.Error("Score must equal ScoreWith(DefaultWeights)")
	}
}

func TestScoreWith_CustomWeightsTune(t *testing.T) {
	in := Input{CVE: "a", CVSS: 9.8, KnownExploited: b(true)} // critical + KEV
	def := ScoreWith(in, DefaultWeights())

	// Down-weight everything → lower score, lower band.
	low := DefaultWeights()
	low.SevCritical = 5
	low.KnownExploited = 2
	got := ScoreWith(in, low)
	if got.Score >= def.Score {
		t.Errorf("down-weighted score should be lower: def=%d custom=%d", def.Score, got.Score)
	}

	// Raise the NOW threshold above the (default-weighted) score → demotes the band.
	strictBand := DefaultWeights()
	strictBand.BandNow = 100
	if ScoreWith(in, strictBand).Priority == PriorityNow && def.Priority == PriorityNow {
		// def is NOW; with BandNow=100 (and score<100) it must drop below NOW.
		if ScoreWith(in, strictBand).Score < 100 {
			t.Errorf("raising BandNow to 100 should demote a sub-100 score out of NOW")
		}
	}
}

func TestScoreWith_ZeroFactorWeights(t *testing.T) {
	in := Input{CVE: "a", CVSS: 9.8, AssetPriority: 5, InternetExposed: b(true),
		KnownExploited: b(true), Dependents: 5}
	// zero factor weights but keep the default band thresholds → score 0, DEFER.
	w := Weights{BandNow: 75, BandSoon: 55, BandScheduled: 35, BandMonitor: 15}
	r := ScoreWith(in, w)
	if r.Score != 0 || r.Priority != PriorityDefer {
		t.Errorf("zero factor weights should yield score 0 / DEFER, got %d / %s", r.Score, r.Priority)
	}
}

func TestSSVC_ActiveExploitExposedIsAct(t *testing.T) {
	r := Score(Input{CVE: "a", CVSS: 9.8, AssetPriority: 5,
		InternetExposed: b(true), KnownExploited: b(true)})
	if r.SSVC.Priority != SSVCAct {
		t.Errorf("active+open+high should be SSVC Act, got %s (%+v)", r.SSVC.Priority, r.SSVC)
	}
	if r.SSVC.Exploitation != "active" || r.SSVC.Exposure != "open" || r.SSVC.Impact != "high" {
		t.Errorf("decision points wrong: %+v", r.SSVC)
	}
}

func TestSSVC_NoExploitInternalLowIsTrack(t *testing.T) {
	r := Score(Input{CVE: "a", CVSS: 5.0, AssetPriority: 1,
		InternetExposed: b(false), KnownExploited: b(false), PublicExploit: b(false)})
	if r.SSVC.Priority != SSVCTrack {
		t.Errorf("none+small+low should be SSVC Track, got %s (%+v)", r.SSVC.Priority, r.SSVC)
	}
}

func TestSSVC_PocExposedHighIsAct(t *testing.T) {
	r := Score(Input{CVE: "a", CVSS: 8.0, AssetPriority: 5,
		InternetExposed: b(true), PublicExploit: b(true), KnownExploited: b(false)})
	if r.SSVC.Priority != SSVCAct {
		t.Errorf("poc+open+high should be Act, got %s", r.SSVC.Priority)
	}
}

func TestSSVC_AutomatableFromHighEPSS(t *testing.T) {
	r := Score(Input{CVE: "a", CVSS: 9.0, EPSS: 0.9, InternetExposed: b(false), KnownExploited: b(true)})
	if r.SSVC.Automatable != "yes" {
		t.Errorf("high EPSS should infer automatable=yes, got %q", r.SSVC.Automatable)
	}
}

func TestScore_EPSSFactorAndUnknown(t *testing.T) {
	// high EPSS adds points + an epss factor.
	hi := Score(Input{CVE: "a", CVSS: 5.0, EPSS: 0.8})
	lo := Score(Input{CVE: "b", CVSS: 5.0, EPSS: 0.05})
	if hi.Score <= lo.Score {
		t.Errorf("high EPSS should outrank near-zero EPSS: hi=%d lo=%d", hi.Score, lo.Score)
	}
	// EPSS unset → surfaced as unknown.
	none := Score(Input{CVE: "c", CVSS: 5.0})
	found := false
	for _, u := range none.Unknowns {
		if u == "exploit probability (EPSS)" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing EPSS should be an unknown, got %+v", none.Unknowns)
	}
}

// ─── severityBucket ───────────────────────────────────────────

func TestSeverityBucket_CVSSPriority(t *testing.T) {
	cases := []struct{ cvss float64; want string }{
		{9.0, "critical"},
		{9.5, "critical"},
		{7.0, "high"},
		{7.5, "high"},
		{4.0, "medium"},
		{5.0, "medium"},
		{1.0, "low"},
		{3.9, "low"},
	}
	for _, tc := range cases {
		if got := severityBucket(tc.cvss, ""); got != tc.want {
			t.Errorf("severityBucket(%.1f, '') = %q, want %q", tc.cvss, got, tc.want)
		}
	}
}

func TestSeverityBucket_StringFallback(t *testing.T) {
	cases := []struct{ sev, want string }{
		{"critical", "critical"},
		{"CRITICAL", "critical"},
		{"high", "high"},
		{"HIGH", "high"},
		{"medium", "medium"},
		{"moderate", "medium"},
		{"low", "low"},
		{"unknown", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := severityBucket(0, tc.sev); got != tc.want {
			t.Errorf("severityBucket(0, %q) = %q, want %q", tc.sev, got, tc.want)
		}
	}
}

// ─── ssvcAutomatable ─────────────────────────────────────────

func TestSSVCAutomatable_ExplicitFalse(t *testing.T) {
	no := false
	r := Score(Input{CVE: "a", CVSS: 7.0, Automatable: &no})
	if r.SSVC.Automatable != "no" {
		t.Errorf("explicit Automatable=false should yield 'no', got %q", r.SSVC.Automatable)
	}
}

func TestSSVCAutomatable_ExplicitTrue(t *testing.T) {
	yes := true
	r := Score(Input{CVE: "a", CVSS: 7.0, Automatable: &yes})
	if r.SSVC.Automatable != "yes" {
		t.Errorf("explicit Automatable=true should yield 'yes', got %q", r.SSVC.Automatable)
	}
}

// ─── ssvcDecide corner cases ─────────────────────────────────

func TestSSVC_PocLowExposureIsMediumTrackStar(t *testing.T) {
	// poc + not-high impact + not-open + automatable → TrackStar via "medium" impact
	r := Score(Input{CVE: "a", CVSS: 5.0, AssetPriority: 2,
		InternetExposed: b(false), PublicExploit: b(true), KnownExploited: b(false)})
	if r.SSVC.Priority != SSVCTrackStar && r.SSVC.Priority != SSVCAttend {
		t.Errorf("poc+medium+small should be TrackStar or Attend, got %s (%+v)",
			r.SSVC.Priority, r.SSVC)
	}
}

func TestSSVC_PocLowAllIsTrack(t *testing.T) {
	// poc + low impact + small exposure + no-auto → Track
	r := Score(Input{CVE: "a", CVSS: 2.0, AssetPriority: 1,
		InternetExposed: b(false), PublicExploit: b(true), KnownExploited: b(false),
		Automatable: b(false)})
	if r.SSVC.Priority != SSVCTrack {
		t.Errorf("poc+low+small+no-auto should be Track, got %s", r.SSVC.Priority)
	}
}

func TestSSVC_NoneHighOpenIsAttend(t *testing.T) {
	// no exploitation, high impact, open exposure → Attend
	r := Score(Input{CVE: "a", CVSS: 9.0, AssetPriority: 5,
		InternetExposed: b(true), KnownExploited: b(false), PublicExploit: b(false)})
	if r.SSVC.Priority != SSVCAct && r.SSVC.Priority != SSVCAttend {
		t.Errorf("none+high+open should be Act or Attend, got %s", r.SSVC.Priority)
	}
}


// TestSeverityBucket_Important closes a vocabulary fail-open: RedHat/Microsoft
// advisories label High-severity issues "Important". Previously this returned
// "" → zero severity weight → the vuln was silently under-ranked. Mirrors the
// existing "moderate" -> medium alias.
func TestSeverityBucket_Important(t *testing.T) {
	if got := severityBucket(0, "important"); got != "high" {
		t.Errorf(`severityBucket(0, "important") = %q, want "high"`, got)
	}
	if got := severityBucket(0, "IMPORTANT"); got != "high" {
		t.Errorf(`severityBucket(0, "IMPORTANT") = %q, want "high"`, got)
	}
}

// TestScore_UnrecognizedSeverity_DistinctMessage: a provided-but-unrecognized
// severity (typo) must be surfaced as such, not conflated with "no severity
// provided" — otherwise the operator thinks they forgot to set it.
func TestScore_UnrecognizedSeverity_DistinctMessage(t *testing.T) {
	r := Score(Input{CVE: "CVE-2026-0009", Severity: "criticl"})
	var sawNotRecognized, sawNotProvided bool
	for _, u := range r.Unknowns {
		if strings.Contains(u, "not recognized") {
			sawNotRecognized = true
		}
		if strings.Contains(u, "no CVSS or severity string provided") {
			sawNotProvided = true
		}
	}
	if !sawNotRecognized {
		t.Errorf(`expected an "not recognized" unknown for a typo'd severity, got %v`, r.Unknowns)
	}
	if sawNotProvided {
		t.Errorf(`must not claim "no severity provided" when one was provided, got %v`, r.Unknowns)
	}
}
