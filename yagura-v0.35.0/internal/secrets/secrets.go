// Package secrets はパスフレーズで AES-256-GCM 暗号化された
// ローカル secret store を提供する。
//
// 設計判断(security spec S0.2):
//   - age 形式互換ではない(age は ChaCha20-Poly1305 + scrypt + bech32 で
//     これら全てが ADR-0001 のゼロ依存ルールに反する第三者ライブラリ依存になる)
//   - 代わりに「シンプルで監査可能な」自前形式を採用
//   - AES-256-GCM (標準ライブラリ crypto/aes + crypto/cipher) で機密性 + 完全性
//   - PBKDF2-HMAC-SHA256 (自前 20 行、crypto/hmac + crypto/sha256) でキー導出
//   - パスフレーズは 12 文字以上を強制
//   - ファイル形式は self-describing(versioned header + base64 body)
//
// ファイル形式 (v1):
//
//	YAGURA-SECRET-V1
//	iter=600000
//	salt=<base64(16 bytes)>
//	nonce=<base64(12 bytes)>
//	data=<base64(AES-256-GCM(plaintext))>
//
// PBKDF2 iteration count 600,000 は OWASP 2023+ の推奨値。
package secrets

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shizukutanaka/yagura/internal/atomicfile"
)

const (
	formatHeader = "YAGURA-SECRET-V1"
	defaultIter  = 600_000 // OWASP PBKDF2-SHA256 recommendation
	// testIter は **テスト専用** の低コスト反復回数。unexported なので公開 API から
	// は選べない——本番の Encrypt は常に defaultIter を使う
	// (TestEncrypt_AlwaysUsesProductionIterations が固定)。
	// 反復回数はファイル header に刻まれるので、安い経路で作った暗号文も
	// 通常の Decrypt でそのまま復号できる(形式変更なし)。
	testIter      = 1_000
	saltSize      = 16
	nonceSize     = 12      // GCM standard
	keySize       = 32      // AES-256
	minPassphrase = 12      // OWASP minimum
	maxFileSize   = 1 << 20 // 1 MB; secrets shouldn't be larger
)

// Errors exposed for caller pattern matching.
var (
	// ErrPassphraseTooShort はパスフレーズが 12 文字未満の場合のエラー。
	ErrPassphraseTooShort = errors.New("secrets: passphrase must be at least 12 characters")
	// ErrInvalidFormat はファイルのフォーマットが不正な場合のエラー。
	ErrInvalidFormat = errors.New("secrets: invalid file format")
	// ErrAuthenticationFailed はパスフレーズが不正または暗号化ファイルが破損している場合のエラー。
	ErrAuthenticationFailed = errors.New("secrets: authentication failed (wrong passphrase or corrupted file)")
)

// Encrypt は plaintext を passphrase で暗号化し、format header を含む
// テキストファイル形式の bytes を返す。
//
// 同一の plaintext + passphrase でも salt と nonce が毎回ランダムなため
// 異なる出力になる。再現可能な暗号化が必要な場合は別途。
func Encrypt(plaintext []byte, passphrase string) ([]byte, error) {
	return encryptWithIter(plaintext, passphrase, defaultIter)
}

// encryptWithIter は反復回数を指定して暗号化する。**unexported**——テストが
// 本番の鍵導出コスト(600,000 回 ≒ 0.3 秒/呼び出し)を払わずに済ませるためだけに在る。
// 公開面に出すと「速いから」と本番で低い値が選ばれうるので出さない。
func encryptWithIter(plaintext []byte, passphrase string, iter int) ([]byte, error) {
	if len(passphrase) < minPassphrase {
		return nil, ErrPassphraseTooShort
	}

	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return nil, fmt.Errorf("read salt: %w", err)
	}

	key := pbkdf2Key([]byte(passphrase), salt, iter, keySize)

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("read nonce: %w", err)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}

	// GCM の AAD として format header を含めることで、header 改ざんを検知する
	ciphertext := gcm.Seal(nil, nonce, plaintext, []byte(formatHeader))

	var buf bytes.Buffer
	fmt.Fprintln(&buf, formatHeader)
	fmt.Fprintf(&buf, "iter=%d\n", iter)
	fmt.Fprintf(&buf, "salt=%s\n", base64.StdEncoding.EncodeToString(salt))
	fmt.Fprintf(&buf, "nonce=%s\n", base64.StdEncoding.EncodeToString(nonce))
	fmt.Fprintf(&buf, "data=%s\n", base64.StdEncoding.EncodeToString(ciphertext))
	return buf.Bytes(), nil
}

// Decrypt は Encrypt の出力を passphrase で復号する。
// passphrase が異なる or ファイル改ざんありの場合は ErrAuthenticationFailed を返す。
func Decrypt(data []byte, passphrase string) ([]byte, error) {
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("secrets: file too large (>%d bytes)", maxFileSize)
	}

	hdr, err := parseHeader(data)
	if err != nil {
		return nil, err
	}

	key := pbkdf2Key([]byte(passphrase), hdr.salt, hdr.iter, keySize)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}

	plaintext, err := gcm.Open(nil, hdr.nonce, hdr.data, []byte(formatHeader))
	if err != nil {
		return nil, ErrAuthenticationFailed
	}
	return plaintext, nil
}

// ─── file-based store (yagura secret CLI) ────────────────────

// Store はファイルシステム上の secret store。
// 各 secret は <dir>/<name>.enc に保存される。name は path traversal を防ぐため正規化される。
type Store struct {
	dir string
	// iter は鍵導出の反復回数。0 は defaultIter(本番)を意味する。
	// **unexported かつ NewStore からは設定できない**——テストだけが安い値を使える。
	iter int
}

// NewStore は指定ディレクトリ配下に store を構築する。
// ディレクトリが存在しなければ 0700 で作成する。
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("mkdir: %w", err)
	}
	return &Store{dir: dir, iter: defaultIter}, nil
}

// Set は name の secret を passphrase で暗号化して保存する。
// 既存ファイルは atomic に置き換えられる(temp + rename)。
func (s *Store) Set(name string, plaintext []byte, passphrase string) error {
	if err := validateName(name); err != nil {
		return err
	}
	iter := s.iter
	if iter == 0 {
		iter = defaultIter
	}
	encrypted, err := encryptWithIter(plaintext, passphrase, iter)
	if err != nil {
		return err
	}
	if err := atomicfile.Write(s.path(name), encrypted, 0o600); err != nil {
		return fmt.Errorf("write secret: %w", err)
	}
	return nil
}

// Get は name の secret を passphrase で復号して返す。
// 存在しない場合は os.ErrNotExist を返す。
func (s *Store) Get(name string, passphrase string) ([]byte, error) {
	if err := validateName(name); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(s.path(name))
	if err != nil {
		return nil, err
	}
	return Decrypt(data, passphrase)
}

// List はディレクトリ内の secret 名(.enc を除く)をアルファベット順で返す。
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		n := e.Name()
		if !strings.HasSuffix(n, ".enc") {
			continue
		}
		out = append(out, strings.TrimSuffix(n, ".enc"))
	}
	return out, nil
}

// Delete は name の secret を削除する。存在しない場合は nil を返す(idempotent)。
func (s *Store) Delete(name string) error {
	if err := validateName(name); err != nil {
		return err
	}
	err := os.Remove(s.path(name))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func (s *Store) path(name string) string {
	return filepath.Join(s.dir, name+".enc")
}

// validateName は path traversal、空文字、制御文字を拒否する。
// 許可文字: a-z A-Z 0-9 _ - .
func validateName(name string) error {
	if name == "" {
		return errors.New("secrets: name is required")
	}
	if len(name) > 200 {
		return errors.New("secrets: name too long")
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '_' || r == '-' || r == '.'
		if !ok {
			return fmt.Errorf("secrets: invalid character in name: %q", r)
		}
	}
	if strings.HasPrefix(name, ".") || strings.HasPrefix(name, "-") {
		return errors.New("secrets: name cannot start with . or -")
	}
	return nil
}

// ─── header parsing ──────────────────────────────────────────

type header struct {
	iter  int
	salt  []byte
	nonce []byte
	data  []byte
}

func parseHeader(b []byte) (*header, error) {
	scanner := newLineScanner(b)
	first, ok := scanner.next()
	if !ok || strings.TrimSpace(first) != formatHeader {
		return nil, ErrInvalidFormat
	}

	var h header
	for {
		line, ok := scanner.next()
		if !ok {
			break
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, found := strings.Cut(line, "=")
		if !found {
			return nil, fmt.Errorf("%w: malformed line", ErrInvalidFormat)
		}
		switch key {
		case "iter":
			n, err := strconv.Atoi(val)
			if err != nil || n < 1000 || n > 10_000_000 {
				return nil, fmt.Errorf("%w: invalid iter", ErrInvalidFormat)
			}
			h.iter = n
		case "salt":
			b, err := base64.StdEncoding.DecodeString(val)
			if err != nil || len(b) != saltSize {
				return nil, fmt.Errorf("%w: invalid salt", ErrInvalidFormat)
			}
			h.salt = b
		case "nonce":
			b, err := base64.StdEncoding.DecodeString(val)
			if err != nil || len(b) != nonceSize {
				return nil, fmt.Errorf("%w: invalid nonce", ErrInvalidFormat)
			}
			h.nonce = b
		case "data":
			b, err := base64.StdEncoding.DecodeString(val)
			if err != nil {
				return nil, fmt.Errorf("%w: invalid data", ErrInvalidFormat)
			}
			h.data = b
		}
	}
	if h.iter == 0 || h.salt == nil || h.nonce == nil || h.data == nil {
		return nil, fmt.Errorf("%w: missing required fields", ErrInvalidFormat)
	}
	return &h, nil
}

// ─── PBKDF2-HMAC-SHA256 (RFC 8018) ───────────────────────────

// pbkdf2Key は RFC 8018 § 5.2 の PBKDF2 を SHA-256 で実装する。
// 標準ライブラリには PBKDF2 がないため自前実装。
//
// password: パスフレーズ
// salt:    ランダムな salt
// iter:    反復回数(高いほど安全だが遅い)
// keyLen:  必要なキー長(bytes)
func pbkdf2Key(password, salt []byte, iter, keyLen int) []byte {
	hashLen := sha256.Size // 32
	numBlocks := (keyLen + hashLen - 1) / hashLen

	out := make([]byte, 0, numBlocks*hashLen)
	block := make([]byte, 4)
	for i := 1; i <= numBlocks; i++ {
		binary.BigEndian.PutUint32(block, uint32(i))

		mac := hmac.New(sha256.New, password)
		mac.Write(salt)
		mac.Write(block)
		u := mac.Sum(nil)
		t := make([]byte, hashLen)
		copy(t, u)

		for j := 2; j <= iter; j++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(nil)
			xorBytes(t, t, u)
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// xorBytes は dst = a XOR b を計算する(len(a) == len(b) == len(dst) 前提)。
// constant-time XOR は不要(出力サイドチャネルではない).
func xorBytes(dst, a, b []byte) {
	for i := range a {
		dst[i] = a[i] ^ b[i]
	}
}

// ─── small line scanner (avoiding bufio for simplicity) ──────

type lineScanner struct {
	data []byte
	pos  int
}

func newLineScanner(b []byte) *lineScanner { return &lineScanner{data: b} }

func (s *lineScanner) next() (string, bool) {
	if s.pos >= len(s.data) {
		return "", false
	}
	end := bytes.IndexByte(s.data[s.pos:], '\n')
	var line []byte
	if end < 0 {
		line = s.data[s.pos:]
		s.pos = len(s.data)
	} else {
		line = s.data[s.pos : s.pos+end]
		s.pos += end + 1
	}
	return string(line), true
}

// ─── utility ─────────────────────────────────────────────────

// EqualBytes は constant-time な []byte 比較。
// passphrase 比較等で timing attack を防ぐ。
func EqualBytes(a, b []byte) bool {
	return subtle.ConstantTimeCompare(a, b) == 1
}

// (compiler hint to keep io import in case it's needed)
var _ = io.EOF
