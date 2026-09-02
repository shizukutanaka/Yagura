// tokens.go: per-owner PAT credential separation (security spec S0.1).
//
// 単一 token を全 repo で使うと、1 トークン compromise で portfolio 全体の
// blast radius になる。owner ごとに別 PAT を持てるようにすることで:
//   - shizukutanaka/* と his-employer/* で credentials を分離
//   - fine-grained PAT を repo set 単位で発行
//   - 1 トークン漏洩 = 1 owner のみ影響
package github

import (
	"regexp"
	"strings"
)

// TokenStore は repo path → token の選択を行う(per-owner credential separation)。
//
// 設計:
//   - perOwner: owner 名(lowercase) → 専用 token
//   - fallback: マッチしない場合の既定 token(空でも可、その場合 unauthenticated)
//
// 並行安全: 構築時に全 token 登録し、以後は read-only として使う。
type TokenStore struct {
	perOwner map[string]string
	fallback string
}

// NewTokenStore は fallback token を持つ store を生成する。
func NewTokenStore(fallback string) *TokenStore {
	return &TokenStore{
		perOwner: map[string]string{},
		fallback: fallback,
	}
}

// AddOwnerToken は指定 owner に専用 token を割り当てる。
// owner は case-insensitive で正規化される。
func (s *TokenStore) AddOwnerToken(owner, token string) {
	if owner == "" || token == "" {
		return
	}
	s.perOwner[strings.ToLower(owner)] = token
}

// TokenForOwner は owner に対応する token を返す。なければ fallback。
func (s *TokenStore) TokenForOwner(owner string) string {
	if t, ok := s.perOwner[strings.ToLower(owner)]; ok {
		return t
	}
	return s.fallback
}

// TokenForPath は API path から owner を抽出して token を返す。
// path が "/repos/{owner}/{repo}/..." 形式でなければ fallback。
func (s *TokenStore) TokenForPath(path string) string {
	if owner := extractOwnerFromPath(path); owner != "" {
		return s.TokenForOwner(owner)
	}
	return s.fallback
}

// HasPerOwner は最低 1 つ以上の owner-specific token があるか返す。
func (s *TokenStore) HasPerOwner() bool {
	return len(s.perOwner) > 0
}

// PerOwnerCount は登録された owner-specific token 数を返す。
func (s *TokenStore) PerOwnerCount() int {
	return len(s.perOwner)
}

// `/repos/{owner}/{repo}/...` の owner 部分を抽出。
var reRepoPath = regexp.MustCompile(`^/repos/([^/]+)/`)

func extractOwnerFromPath(path string) string {
	m := reRepoPath.FindStringSubmatch(path)
	if m == nil {
		return ""
	}
	return m[1]
}
