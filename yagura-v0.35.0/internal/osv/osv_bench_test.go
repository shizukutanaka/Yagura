package osv

import "testing"

func BenchmarkLanguageToEcosystem(b *testing.B) {
	langs := []string{"Go", "Python", "JavaScript", "Rust", "Ruby", "C#"}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = LanguageToEcosystem(langs[i%len(langs)])
	}
}

func BenchmarkParseCVSSBaseScore(b *testing.B) {
	v := "CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = parseCVSSBaseScore(v)
	}
}

func BenchmarkSortVulns(b *testing.B) {
	vulns := []Vuln{
		{ID: "1", Severity: SeverityMedium},
		{ID: "2", Severity: SeverityCritical},
		{ID: "3", Severity: SeverityHigh},
		{ID: "4", Severity: SeverityLow},
		{ID: "5", Severity: SeverityHigh},
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		cp := append([]Vuln(nil), vulns...)
		sortVulns(cp)
	}
}
