// Package config は環境変数から Yagura の設定を読込む。
//
// Yagura は個人マシンに常駐する daemon であり、Mihari と違って外部からの
// webhook を受領しない。そのため秘密情報の数は比較的少ない:
//
//	YAGURA_GITHUB_TOKEN     — GitHub API 用 PAT (必須)
//	YAGURA_MCP_TOKEN        — MCP Bearer Token (空なら無認証 = local 限定)
//	YAGURA_STATE_DIR        — state ディレクトリ (default ~/.yagura/state)
//	YAGURA_ADDR             — listen address (default 127.0.0.1:8090)
//	YAGURA_LOG_LEVEL        — debug/info/warn/error (default info)
//	YAGURA_SCAN_INTERVAL    — GitHub poll 間隔 (default 5m)
//	YAGURA_ENABLE_PPROF     — pprof endpoint 有効化 (default false)
//
// listen address は default で 127.0.0.1 — 外部公開しない前提。
// 外部公開する場合は明示的に 0.0.0.0 を指定し、必ず YAGURA_MCP_TOKEN 設定。
package config

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var validLogLevels = map[string]bool{
	"debug": true, "info": true, "warn": true, "error": true,
}

// Config は Yagura 全体の動作設定。
type Config struct {
	Addr                 string
	StateDir             string
	GitHubToken          string            // fallback (YAGURA_GITHUB_TOKEN)
	GitHubTokens         map[string]string // per-owner PATs (YAGURA_GITHUB_TOKEN_<OWNER>) — S0.1
	GitHubBase           string
	MCPToken             string
	LogLevel             string
	ScanInterval         time.Duration
	ScanTimeout          time.Duration
	SecurityScanInterval time.Duration // Scorecard + OSV scan interval(default 24h)
	EnablePprof          bool
	AuditKeepDays        int
}

// String はシークレットを redact した安全な表現を返す。
func (c *Config) String() string {
	return fmt.Sprintf(
		"Config{Addr:%s StateDir:%s LogLevel:%s ScanInterval:%s "+
			"EnablePprof:%v GitHubToken:<%s> MCPToken:<%s>}",
		c.Addr, c.StateDir, c.LogLevel, c.ScanInterval, c.EnablePprof,
		redactStatus(c.GitHubToken), redactStatus(c.MCPToken),
	)
}

// GoString implements fmt.GoStringer for %#v.
func (c *Config) GoString() string { return c.String() }

func redactStatus(secret string) string {
	if secret == "" {
		return "unset"
	}
	return fmt.Sprintf("redacted, %d bytes", len(secret))
}

// Load は環境変数を読み、必須項目欠落 / 形式不正時はエラーを返す。
// 複数エラーは errors.Join でまとめて返す。
func Load() (*Config, error) {
	c := &Config{
		Addr:          envOr("YAGURA_ADDR", "127.0.0.1:8090"),
		LogLevel:      envOr("YAGURA_LOG_LEVEL", "info"),
		GitHubBase:    envOr("YAGURA_GITHUB_BASE", "https://api.github.com"),
		MCPToken:      os.Getenv("YAGURA_MCP_TOKEN"),
		EnablePprof:   envBool("YAGURA_ENABLE_PPROF"),
		AuditKeepDays: envInt("YAGURA_AUDIT_KEEP_DAYS", 90),
	}

	var errs []error

	// StateDir: token 不要な解決ロジックは ResolveStateDir に集約。
	stateDir, sderr := ResolveStateDir()
	if sderr != nil {
		errs = append(errs, sderr)
	}
	c.StateDir = stateDir

	// GitHubToken は **任意**(v1.2.0)。
	//
	// 以前は必須で、無いと daemon が起動しなかった。しかし GitHub / ネットワークを
	// 要するのは vulns・scorecard・scanner だけで、29 レンズ・registry・graph・plan・
	// harness 監査はすべてローカルで完結する。「ローカル優先」を掲げながら、ローカル
	// 作業のために PAT 発行を強制していた。
	//
	// **無いのは選択、壊れているのは事故** なので、空は許し、形が不正なら今も error。
	c.GitHubToken = os.Getenv("YAGURA_GITHUB_TOKEN")
	if c.GitHubToken != "" && !looksLikeGitHubToken(c.GitHubToken) {
		errs = append(errs, errors.New(
			"YAGURA_GITHUB_TOKEN should start with 'ghp_', 'github_pat_', or 'gho_'"))
	}

	// S0.1: per-owner PAT credential separation
	// env var `YAGURA_GITHUB_TOKEN_<OWNER>` 形式を全て収集する。
	// 例: YAGURA_GITHUB_TOKEN_SHIZUKUTANAKA=ghp_xxx → owner "shizukutanaka" 専用
	c.GitHubTokens = map[string]string{}
	for _, env := range os.Environ() {
		eq := strings.IndexByte(env, '=')
		if eq < 0 {
			continue
		}
		name, val := env[:eq], env[eq+1:]
		const prefix = "YAGURA_GITHUB_TOKEN_"
		if !strings.HasPrefix(name, prefix) || len(name) == len(prefix) {
			continue
		}
		owner := strings.ToLower(name[len(prefix):])
		if val == "" {
			continue
		}
		if !looksLikeGitHubToken(val) {
			errs = append(errs, fmt.Errorf(
				"%s should start with 'ghp_', 'github_pat_', or 'gho_'", name))
			continue
		}
		c.GitHubTokens[owner] = val
	}

	// Addr: 形式検証
	if err := validateAddr(c.Addr); err != nil {
		errs = append(errs, fmt.Errorf("YAGURA_ADDR invalid: %w", err))
	}

	// LogLevel: 既知の値のみ
	if !validLogLevels[strings.ToLower(c.LogLevel)] {
		errs = append(errs, fmt.Errorf(
			"YAGURA_LOG_LEVEL must be one of debug/info/warn/error, got %q", c.LogLevel))
	}

	// ScanInterval: default 5m、Parseable duration
	intervalStr := envOr("YAGURA_SCAN_INTERVAL", "5m")
	d, err := time.ParseDuration(intervalStr)
	if err != nil {
		errs = append(errs, fmt.Errorf("YAGURA_SCAN_INTERVAL invalid duration %q: %w", intervalStr, err))
	} else if d < 30*time.Second {
		errs = append(errs, fmt.Errorf(
			"YAGURA_SCAN_INTERVAL too short (%s), minimum 30s to respect GitHub rate limits", d))
	}
	c.ScanInterval = d

	// ScanTimeout: default 30s
	timeoutStr := envOr("YAGURA_SCAN_TIMEOUT", "30s")
	to, terr := time.ParseDuration(timeoutStr)
	if terr != nil {
		errs = append(errs, fmt.Errorf("YAGURA_SCAN_TIMEOUT invalid duration %q: %w", timeoutStr, terr))
	} else if to < 1*time.Second {
		errs = append(errs, fmt.Errorf(
			"YAGURA_SCAN_TIMEOUT too short (%s), minimum 1s", to))
	}
	c.ScanTimeout = to

	// SecurityScanInterval: default 24h、外部 API 負荷を考慮して低頻度に
	secIntervalStr := envOr("YAGURA_SECURITY_SCAN_INTERVAL", "24h")
	sd, serr := time.ParseDuration(secIntervalStr)
	if serr != nil {
		errs = append(errs, fmt.Errorf("YAGURA_SECURITY_SCAN_INTERVAL invalid duration %q: %w", secIntervalStr, serr))
	} else if sd < 1*time.Hour {
		errs = append(errs, fmt.Errorf(
			"YAGURA_SECURITY_SCAN_INTERVAL too short (%s), minimum 1h", sd))
	}
	c.SecurityScanInterval = sd

	// GitHubBase: URL 検証
	if !strings.HasPrefix(c.GitHubBase, "http://") && !strings.HasPrefix(c.GitHubBase, "https://") {
		errs = append(errs, fmt.Errorf(
			"YAGURA_GITHUB_BASE must start with http:// or https://, got %q", c.GitHubBase))
	}

	// 外部公開時のセキュリティ警告(error ではないが return で含めて呼出側で判断可能に)
	// 外部 IP に bind + MCP token なし → 危険な組み合わせ
	if isPublicBind(c.Addr) && c.MCPToken == "" {
		errs = append(errs, errors.New(
			"refusing to start: YAGURA_ADDR binds to a public interface but YAGURA_MCP_TOKEN is empty"))
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return c, nil
}

// looksLikeGitHubToken は GitHub トークンの典型的 prefix をチェックする。
// 完全な validation ではなく、明らかな誤コピーの早期発見が目的。
func looksLikeGitHubToken(t string) bool {
	prefixes := []string{"ghp_", "github_pat_", "gho_", "ghs_", "ghu_"}
	for _, p := range prefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// validateAddr は HTTP server address の形式を検証する。
func validateAddr(addr string) error {
	if addr == "" {
		return errors.New("must not be empty")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("must be 'host:port' or ':port', got %q: %w", addr, err)
	}
	if port == "" {
		return errors.New("port is required")
	}
	p, err := strconv.Atoi(port)
	if err != nil {
		return fmt.Errorf("port must be numeric, got %q", port)
	}
	if p <= 0 || p > 65535 {
		return fmt.Errorf("port must be in 1-65535, got %d", p)
	}
	if strings.ContainsAny(host, " \t\n\r") {
		return fmt.Errorf("host contains whitespace: %q", host)
	}
	return nil
}

// isPublicBind は addr が外部に公開される interface に bind しているかを判定する。
// 127.0.0.1, localhost, [::1] は private 扱い、空ホスト("" or "0.0.0.0", "::") は public 扱い。
func isPublicBind(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false // validateAddr で別途エラーになる
	}
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return false
	case "", "0.0.0.0", "::":
		return true
	}
	// 明示的に IP アドレスを指定した場合: loopback かチェック
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return false
	}
	// それ以外(LAN IP、ホスト名等)は public 扱いの方が安全
	return true
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string) bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	switch v {
	case "true", "1", "yes", "on":
		return true
	}
	return false
}

// ResolveStateDir は GitHub token を要求せずに state directory を解決する。
// YAGURA_STATE_DIR があればそれを、無ければ ~/.yagura/state を返す。
// token 不要な経路(CLI の registry CRUD / local scan、verify subcommand)が
// config.Load() を経由せずに state dir を得るために使う。
func ResolveStateDir() (string, error) {
	if d := os.Getenv("YAGURA_STATE_DIR"); d != "" {
		return d, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve $HOME: %w", err)
	}
	return filepath.Join(home, ".yagura", "state"), nil
}

// ProjectsDirFor は state dir 配下の projects ディレクトリを返す。
func ProjectsDirFor(stateDir string) string { return filepath.Join(stateDir, "projects") }

// AuditDirFor は state dir 配下の audit ディレクトリを返す。
func AuditDirFor(stateDir string) string { return filepath.Join(stateDir, "audit") }

// SecretsDirFor は state dir 配下の secrets ディレクトリを返す。
func SecretsDirFor(stateDir string) string { return filepath.Join(stateDir, "secrets") }

// ProjectsDir は project state ファイル群の置き場を返す。
// 個別ファイルは <ProjectsDir>/<slug>.json として保存される。
func (c *Config) ProjectsDir() string {
	return ProjectsDirFor(c.StateDir)
}

// AuditPath は append-only audit log JSONL の保存先を返す。
// 1 ファイル / 日(YYYY-MM-DD.jsonl)。
func (c *Config) AuditPath() string {
	return AuditDirFor(c.StateDir)
}

// DraftsDir は Yagura が自動生成する PR/Issue draft の保存先を返す。
// draft は Yagura が push せず、人間が確認後に gh CLI 等で送る前提。
func (c *Config) DraftsDir() string {
	return filepath.Join(c.StateDir, "drafts")
}

// SecretsPath は AES-256-GCM 暗号化された secret の保存先を返す。
// 各 secret は <SecretsPath()>/<name>.enc に格納される。
// 操作は yagura secret サブコマンド経由のみ(daemon は触らない)。
func (c *Config) SecretsPath() string {
	return SecretsDirFor(c.StateDir)
}

// envInt は環境変数を int として読む。未設定 or parse 失敗時は def を返す。
// 負の値も許可(呼出側で意味付け)。
func envInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// GitHubEnabled は GitHub API に依存する機能が使えるかを返す。
func (c *Config) GitHubEnabled() bool { return c.GitHubToken != "" }

// DisabledCapabilities は資格情報が無いために **今動かない** 機能名を返す。
//
// 起動時にこれを必ず表示すること。黙って劣化した状態で動く方が、起動を拒否するより
// 質が悪い——利用者は「スキャンした結果 0 件」と「スキャンしていない」を区別できない。
func (c *Config) DisabledCapabilities() []string {
	if c.GitHubEnabled() {
		return nil
	}
	return []string{
		"yagura_vulns (OSV lookups need repository metadata)",
		"yagura_scorecard (OpenSSF Scorecard API)",
		"background scanner (GitHub repository metadata refresh)",
	}
}
