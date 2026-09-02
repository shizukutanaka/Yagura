package osv

import (
	"testing"
)

// FuzzParseCVSSBaseScore は任意の CVSS 文字列で panic しないこと、
// 結果が [0.0, 10.0] の範囲または -1 (parse failure) であることを確認する。
func FuzzParseCVSSBaseScore(f *testing.F) {
	f.Add("")
	f.Add("CVSS:3.1/AV:N/AC:L/PR:N/UI:N/S:U/C:H/I:H/A:H")
	f.Add("CVSS:2.0/AV:N/AC:M/Au:N/C:P/I:P/A:P")
	f.Add("garbage")
	f.Add("CVSS:9.0")
	f.Add("AV:N")

	f.Fuzz(func(t *testing.T, v string) {
		score := parseCVSSBaseScore(v)
		// Property: スコアは -1(無効) or [0, 10] の範囲
		if score != -1 && (score < 0 || score > 10.0) {
			t.Errorf("out-of-range CVSS score %g for %q", score, v)
		}
	})
}

// FuzzLanguageToEcosystem は任意の言語名で panic しないこと、
// 結果が空文字 or 既知 ecosystem 名であることを確認する。
func FuzzLanguageToEcosystem(f *testing.F) {
	f.Add("")
	f.Add("Go")
	f.Add("Python")
	f.Add("unknown-language-12345")

	knownEcosystems := map[string]bool{
		"":          true,
		"Go":        true,
		"PyPI":      true,
		"npm":       true,
		"crates.io": true,
		"RubyGems":  true,
		"Maven":     true,
		"NuGet":     true,
		"Packagist": true,
	}

	f.Fuzz(func(t *testing.T, lang string) {
		eco := LanguageToEcosystem(lang)
		// Property: 結果は既知 ecosystem のひとつ(空文字含む)
		if !knownEcosystems[eco] {
			t.Errorf("unexpected ecosystem %q for language %q", eco, lang)
		}
	})
}
