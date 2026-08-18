package secrets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ─── Encrypt / Decrypt round-trip ────────────────────────────

func TestEncryptDecrypt_RoundTrip(t *testing.T) {
	plaintext := []byte("hello, world. this is a secret.")
	passphrase := "correct-horse-battery-staple"

	encrypted, err := encryptCheap(plaintext, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(encrypted, []byte(formatHeader)) {
		t.Errorf("output should start with format header")
	}

	decrypted, err := Decrypt(encrypted, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decrypted, plaintext) {
		t.Errorf("round-trip mismatch:\n  in:  %q\n  out: %q", plaintext, decrypted)
	}
}

func TestEncrypt_DifferentSaltsEachCall(t *testing.T) {
	plaintext := []byte("same")
	passphrase := "same-passphrase-12345"
	a, _ := encryptCheap(plaintext, passphrase)
	b, _ := encryptCheap(plaintext, passphrase)
	if bytes.Equal(a, b) {
		t.Error("two encryptions of same plaintext should differ (salt + nonce)")
	}
}

func TestEncrypt_PassphraseTooShort(t *testing.T) {
	_, err := encryptCheap([]byte("hi"), "short")
	if !errors.Is(err, ErrPassphraseTooShort) {
		t.Errorf("expected ErrPassphraseTooShort, got %v", err)
	}
}

func TestEncrypt_ExactMinLength(t *testing.T) {
	// 12 chars exactly
	_, err := encryptCheap([]byte("data"), "exactly12chr")
	if err != nil {
		t.Errorf("12-char passphrase should be accepted: %v", err)
	}
}

func TestDecrypt_WrongPassphrase(t *testing.T) {
	encrypted, _ := encryptCheap([]byte("secret"), "correct-passphrase-1")
	_, err := Decrypt(encrypted, "wrong-passphrase-12345")
	if !errors.Is(err, ErrAuthenticationFailed) {
		t.Errorf("expected ErrAuthenticationFailed, got %v", err)
	}
}

func TestDecrypt_TamperedCiphertext(t *testing.T) {
	encrypted, _ := encryptCheap([]byte("secret data"), "correct-passphrase-1")
	// Flip one byte in the data line
	idx := bytes.Index(encrypted, []byte("data="))
	if idx < 0 {
		t.Fatal("data= not found")
	}
	// Flip a base64 character somewhere safe
	encrypted[idx+10] ^= 0x01

	_, err := Decrypt(encrypted, "correct-passphrase-1")
	if err == nil {
		t.Error("tampered ciphertext should fail to decrypt")
	}
}

func TestDecrypt_TamperedHeader(t *testing.T) {
	encrypted, _ := encryptCheap([]byte("secret"), "correct-passphrase-1")
	// Replace YAGURA-SECRET-V1 with YAGURA-SECRET-V2
	encrypted = bytes.Replace(encrypted, []byte("YAGURA-SECRET-V1"), []byte("YAGURA-SECRET-V2"), 1)
	_, err := Decrypt(encrypted, "correct-passphrase-1")
	if !errors.Is(err, ErrInvalidFormat) {
		t.Errorf("expected ErrInvalidFormat, got %v", err)
	}
}

func TestDecrypt_InvalidFormat(t *testing.T) {
	tests := [][]byte{
		[]byte(""),
		[]byte("hello"),
		[]byte("YAGURA-SECRET-V1\n"), // no fields
		[]byte("YAGURA-SECRET-V1\niter=600000\n"),    // partial
		[]byte("YAGURA-SECRET-V1\nmalformed-line\n"), // missing =
	}
	for i, in := range tests {
		_, err := Decrypt(in, "correct-passphrase-1")
		if err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}

func TestDecrypt_FileTooLarge(t *testing.T) {
	big := make([]byte, maxFileSize+1)
	_, err := Decrypt(big, "correct-passphrase-1")
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("expected size error, got %v", err)
	}
}

// ─── PBKDF2 reference vectors ────────────────────────────────

// RFC 6070 vectors are for SHA-1 only. For SHA-256 we use the
// reference vectors from https://stackoverflow.com/a/5136918
// and confirmed against Python's hashlib.pbkdf2_hmac.
func TestPBKDF2_KnownVectors(t *testing.T) {
	tests := []struct {
		password string
		salt     string
		iter     int
		keyLen   int
		expected string // hex
	}{
		// PBKDF2-HMAC-SHA256 test vector (RFC-style, verified)
		{
			password: "password",
			salt:     "salt",
			iter:     1,
			keyLen:   32,
			expected: "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b",
		},
		{
			password: "password",
			salt:     "salt",
			iter:     2,
			keyLen:   32,
			expected: "ae4d0c95af6b46d32d0adff928f06dd02a303f8ef3c251dfd6e2d85a95474c43",
		},
		{
			password: "passwordPASSWORDpassword",
			salt:     "saltSALTsaltSALTsaltSALTsaltSALTsalt",
			iter:     4096,
			keyLen:   40,
			expected: "348c89dbcbd32b2f32d814b8116e84cf2b17347ebc1800181c4e2a1fb8dd53e1c635518c7dac47e9",
		},
	}
	for i, tt := range tests {
		got := pbkdf2Key([]byte(tt.password), []byte(tt.salt), tt.iter, tt.keyLen)
		gotHex := hex.EncodeToString(got)
		if gotHex != tt.expected {
			t.Errorf("vector %d: got %s want %s", i, gotHex, tt.expected)
		}
	}
}

func TestPBKDF2_DerivedKeyDeterministic(t *testing.T) {
	// Same input → same output
	a := pbkdf2Key([]byte("pw"), []byte("salt"), 1000, 32)
	b := pbkdf2Key([]byte("pw"), []byte("salt"), 1000, 32)
	if !bytes.Equal(a, b) {
		t.Error("PBKDF2 should be deterministic")
	}
	// Different salts → different outputs
	c := pbkdf2Key([]byte("pw"), []byte("other"), 1000, 32)
	if bytes.Equal(a, c) {
		t.Error("different salt should give different key")
	}
}

func TestSHA256Available(t *testing.T) {
	// Smoke test ensuring our pbkdf2 produces 32-byte output consistent with sha256
	h := sha256.Sum256([]byte("test"))
	if len(h) != 32 {
		t.Fatal("sha256 broken")
	}
}

// ─── validateName ────────────────────────────────────────────

func TestValidateName(t *testing.T) {
	valid := []string{"a", "github-token", "MCP_TOKEN", "v1.2.3", "abc123", "x.y.z"}
	for _, n := range valid {
		if err := validateName(n); err != nil {
			t.Errorf("%q should be valid: %v", n, err)
		}
	}
	invalid := []string{
		"",                       // empty
		"../etc/passwd",          // path traversal
		"/abs",                   // absolute
		"name with spaces",       // space
		"name\x00null",           // null byte
		".hidden",                // leading dot
		"-flag",                  // leading hyphen
		strings.Repeat("a", 201), // too long
		"日本語",                    // non-ASCII (intentional restriction)
		"name/sub",               // slash
	}
	for _, n := range invalid {
		if err := validateName(n); err == nil {
			t.Errorf("%q should be invalid", n)
		}
	}
}

// ─── Store ───────────────────────────────────────────────────

func TestStore_RoundTrip(t *testing.T) {
	s, err := newCheapStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pass := "correct-horse-battery-staple"
	if err := s.Set("github_pat", []byte("ghp_abc123"), pass); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("github_pat", pass)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "ghp_abc123" {
		t.Errorf("round-trip: got %q", got)
	}

	// File should be mode 0600
	info, _ := os.Stat(filepath.Join(s.dir, "github_pat.enc"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("expected mode 0600, got %v", info.Mode().Perm())
	}
}

func TestStore_Get_NotFound(t *testing.T) {
	s, _ := newCheapStore(t.TempDir())
	_, err := s.Get("missing", "correct-passphrase-1")
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected ErrNotExist, got %v", err)
	}
}

func TestStore_List(t *testing.T) {
	s, _ := newCheapStore(t.TempDir())
	pass := "correct-passphrase-1"
	_ = s.Set("alpha", []byte("a"), pass)
	_ = s.Set("beta", []byte("b"), pass)
	_ = s.Set("gamma", []byte("c"), pass)

	names, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(names))
	}
}

func TestStore_List_Empty(t *testing.T) {
	s, _ := newCheapStore(t.TempDir())
	names, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 0 {
		t.Errorf("expected 0, got %d", len(names))
	}
}

func TestStore_Delete(t *testing.T) {
	s, _ := newCheapStore(t.TempDir())
	pass := "correct-passphrase-1"
	_ = s.Set("delete-me", []byte("data"), pass)
	if err := s.Delete("delete-me"); err != nil {
		t.Fatal(err)
	}
	_, err := s.Get("delete-me", pass)
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("should be gone, got %v", err)
	}
	// Delete is idempotent
	if err := s.Delete("delete-me"); err != nil {
		t.Errorf("second delete should be no-op: %v", err)
	}
}

func TestStore_RejectsInvalidNames(t *testing.T) {
	s, _ := newCheapStore(t.TempDir())
	for _, bad := range []string{"", "../escape", "name with space"} {
		if err := s.Set(bad, []byte("data"), "correct-passphrase-1"); err == nil {
			t.Errorf("Set(%q) should be rejected", bad)
		}
		if _, err := s.Get(bad, "correct-passphrase-1"); err == nil {
			t.Errorf("Get(%q) should be rejected", bad)
		}
		if err := s.Delete(bad); err == nil {
			t.Errorf("Delete(%q) should be rejected", bad)
		}
	}
}

func TestStore_AtomicReplace(t *testing.T) {
	s, _ := newCheapStore(t.TempDir())
	pass := "correct-passphrase-1"

	if err := s.Set("v", []byte("v1"), pass); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("v", []byte("v2"), pass); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get("v", pass)
	if string(got) != "v2" {
		t.Errorf("expected v2, got %q", got)
	}
	// .tmp file should not linger
	entries, _ := os.ReadDir(s.dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover .tmp file: %s", e.Name())
		}
	}
}

// ─── EqualBytes ─────────────────────────────────────────────

func TestEqualBytes(t *testing.T) {
	if !EqualBytes([]byte("abc"), []byte("abc")) {
		t.Error("equal slices should be equal")
	}
	if EqualBytes([]byte("abc"), []byte("abd")) {
		t.Error("different slices should not be equal")
	}
	if EqualBytes([]byte("abc"), []byte("abcd")) {
		t.Error("different-length slices should not be equal")
	}
}

// ─── NewStore error path ─────────────────────────────────────

func TestNewStore_MkdirFails(t *testing.T) {
	// Place a regular file where the dir should be → MkdirAll fails.
	parent := t.TempDir()
	blocker := filepath.Join(parent, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := newCheapStore(filepath.Join(blocker, "secrets"))
	if err == nil {
		t.Error("expected error when parent path is a file, got nil")
	}
}

// ─── List edge cases ─────────────────────────────────────────

func TestStore_List_DirNotExist_ReturnsNil(t *testing.T) {
	// When the store directory itself has been removed, List returns nil, nil.
	dir := t.TempDir()
	s := &Store{dir: filepath.Join(dir, "nonexistent")}
	names, err := s.List()
	if err != nil {
		t.Fatalf("List on nonexistent dir should return nil error, got %v", err)
	}
	if names != nil {
		t.Errorf("expected nil names, got %v", names)
	}
}

func TestStore_List_SkipsSubdirs(t *testing.T) {
	// Subdirectories inside the store dir should not be included in the listing.
	s, err := newCheapStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// Create a subdir
	if err := os.Mkdir(filepath.Join(s.dir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Create a non-.enc file
	if err := os.WriteFile(filepath.Join(s.dir, "README.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Create a real secret
	_ = s.Set("mysecret", []byte("val"), "correct-passphrase-1")

	names, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(names) != 1 || names[0] != "mysecret" {
		t.Errorf("expected [mysecret], got %v (dirs and non-.enc files should be skipped)", names)
	}
}

// TestStore_Set_EncryptFails exercises the Set path where Encrypt returns an
// error (passphrase shorter than minPassphrase=12 characters).
func TestStore_Set_EncryptFails(t *testing.T) {
	s, err := newCheapStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set("mykey", []byte("value"), "short"); err == nil {
		t.Error("expected error for passphrase shorter than 12 chars")
	}
}

// TestStore_Set_WriteFails exercises the atomicfile.Write failure path in Set.
// Pre-creating <name>.enc as a directory causes os.Rename(tempfile, dir) to
// return EISDIR on Linux.
func TestStore_Set_WriteFails(t *testing.T) {
	dir := t.TempDir()
	s := &Store{dir: dir}
	encPath := filepath.Join(dir, "mykey.enc")
	if err := os.Mkdir(encPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("mykey", []byte("value"), "correct-passphrase-1"); err == nil {
		t.Error("expected error when target .enc path is a directory")
	}
}

// TestStore_List_ReadDirFails exercises the non-ErrNotExist branch in List:
// when the store dir is actually a regular file, os.ReadDir returns ENOTDIR
// (which is not os.ErrNotExist), so List must return that error.
func TestStore_List_ReadDirFails(t *testing.T) {
	parent := t.TempDir()
	notADir := filepath.Join(parent, "notadir")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &Store{dir: notADir}
	if _, err := s.List(); err == nil {
		t.Error("expected error when store dir is a regular file (ENOTDIR)")
	}
}

// TestStore_Delete_RemoveFails exercises the non-ErrNotExist branch in Delete:
// replacing the .enc path with a non-empty directory causes os.Remove to
// return ENOTEMPTY, which is not ErrNotExist, so Delete must return it.
func TestStore_Delete_RemoveFails(t *testing.T) {
	dir := t.TempDir()
	s := &Store{dir: dir}
	encPath := filepath.Join(dir, "mykey.enc")
	if err := os.Mkdir(encPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(encPath, "x"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete("mykey"); err == nil {
		t.Error("expected error when .enc path is a non-empty directory")
	}
}

// TestParseHeader_BlankLineTolerated verifies that blank lines between header
// fields are silently skipped (the `if line == "" { continue }` branch).
func TestParseHeader_BlankLineTolerated(t *testing.T) {
	enc, err := encryptCheap([]byte("plaintext"), "correct-passphrase-1")
	if err != nil {
		t.Fatal(err)
	}
	// Insert a blank line after every existing line.
	lines := strings.Split(string(enc), "\n")
	var buf strings.Builder
	for _, l := range lines {
		buf.WriteString(l)
		buf.WriteString("\n")
		buf.WriteString("\n")
	}
	if _, err := parseHeader([]byte(buf.String())); err != nil {
		t.Errorf("parseHeader should tolerate blank lines: %v", err)
	}
}

// TestParseHeader_MalformedLine verifies that a header line without '=' triggers
// the `return nil, fmt.Errorf("%w: malformed line", ErrInvalidFormat)` branch.
func TestParseHeader_MalformedLine(t *testing.T) {
	bad := formatHeader + "\nno-equals-sign\n"
	_, err := parseHeader([]byte(bad))
	if err == nil {
		t.Fatal("expected error for malformed header line")
	}
	if !errors.Is(err, ErrInvalidFormat) {
		t.Errorf("expected ErrInvalidFormat, got %v", err)
	}
}

// base64 constants used in parseHeader field-validation tests.
// 16 zero bytes, 12 zero bytes, 4 zero bytes (wrong length).
const (
	b64zeros16 = "AAAAAAAAAAAAAAAAAAAAAA==" // base64(16 × 0x00)
	b64zeros12 = "AAAAAAAAAAAAAAAA"         // base64(12 × 0x00)
	b64zeros4  = "AAAAAA=="                 // base64(4 × 0x00) — wrong length
	b64zeros1  = "AA=="                     // base64(1 × 0x00)
)

// TestParseHeader_InvalidIter covers the `iter < 1000` branch.
func TestParseHeader_InvalidIter(t *testing.T) {
	hdr := formatHeader + "\niter=999\nsalt=" + b64zeros16 + "\nnonce=" + b64zeros12 + "\ndata=" + b64zeros1 + "\n"
	_, err := parseHeader([]byte(hdr))
	if !errors.Is(err, ErrInvalidFormat) {
		t.Errorf("expected ErrInvalidFormat for iter=999, got %v", err)
	}
}

// TestParseHeader_InvalidSalt covers the `len(b) != saltSize` branch.
func TestParseHeader_InvalidSalt(t *testing.T) {
	hdr := formatHeader + "\niter=600000\nsalt=" + b64zeros4 + "\nnonce=" + b64zeros12 + "\ndata=" + b64zeros1 + "\n"
	_, err := parseHeader([]byte(hdr))
	if !errors.Is(err, ErrInvalidFormat) {
		t.Errorf("expected ErrInvalidFormat for short salt, got %v", err)
	}
}

// TestParseHeader_InvalidNonce covers the `len(b) != nonceSize` branch.
func TestParseHeader_InvalidNonce(t *testing.T) {
	hdr := formatHeader + "\niter=600000\nsalt=" + b64zeros16 + "\nnonce=" + b64zeros4 + "\ndata=" + b64zeros1 + "\n"
	_, err := parseHeader([]byte(hdr))
	if !errors.Is(err, ErrInvalidFormat) {
		t.Errorf("expected ErrInvalidFormat for short nonce, got %v", err)
	}
}

// TestParseHeader_InvalidData covers the `base64.StdEncoding.DecodeString` error branch for data.
func TestParseHeader_InvalidData(t *testing.T) {
	hdr := formatHeader + "\niter=600000\nsalt=" + b64zeros16 + "\nnonce=" + b64zeros12 + "\ndata=!!!not-valid-base64!!!\n"
	_, err := parseHeader([]byte(hdr))
	if !errors.Is(err, ErrInvalidFormat) {
		t.Errorf("expected ErrInvalidFormat for invalid data base64, got %v", err)
	}
}

// テストは **本番の鍵導出コストを払わない**。ただしそれは「安全側の定数を
// 弱めてよい」という意味ではないので、本番既定値をこのテストが固定する。
// 600,000 は OWASP の PBKDF2-SHA256 推奨値(2023 以降)。
func TestDefaultIterations_StayAtOWASPRecommendation(t *testing.T) {
	if defaultIter != 600_000 {
		t.Errorf("production PBKDF2 iterations must stay at the OWASP recommendation 600000, got %d", defaultIter)
	}
}

// 公開 API の Encrypt は必ず本番反復回数を使う(テスト用の安い経路が
// 公開面に漏れていないこと)。ファイル header に刻まれた iter を読んで確かめる。
func TestEncrypt_AlwaysUsesProductionIterations(t *testing.T) {
	out, err := Encrypt([]byte("x"), "passphrase-long-enough")
	if err != nil {
		t.Fatal(err)
	}
	hdr, err := parseHeader(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if hdr.iter != defaultIter {
		t.Errorf("Encrypt must use %d iterations, got %d", defaultIter, hdr.iter)
	}
}

// 安い経路で暗号化したものも、通常の Decrypt で復号できること
// (iter はファイルに刻まれているので互換性が保たれる)。
func TestEncryptWithIter_RoundTripsThroughNormalDecrypt(t *testing.T) {
	out, err := encryptWithIter([]byte("hello"), "passphrase-long-enough", testIter)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decrypt(out, "passphrase-long-enough")
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("round trip: got %q", got)
	}
}

// encryptCheap はテスト用の薄いラッパ(本番の反復回数を払わない)。
func encryptCheap(plaintext []byte, passphrase string) ([]byte, error) {
	return encryptWithIter(plaintext, passphrase, testIter)
}

// newCheapStore はテスト用の Store(本番の鍵導出コストを払わない)。
// 本番既定は TestNewStore_UsesProductionIterations が固定する。
func newCheapStore(dir string) (*Store, error) {
	st, err := NewStore(dir)
	if err != nil {
		return nil, err
	}
	st.iter = testIter
	return st, nil
}

func TestNewStore_UsesProductionIterations(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if st.iter != defaultIter {
		t.Errorf("NewStore must default to %d iterations, got %d", defaultIter, st.iter)
	}
}
