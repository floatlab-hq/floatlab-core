// Package credentials contains the installer and control plane's credential format.
package credentials

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

type Bootstrap struct {
	Username       string   `json:"username"`
	PasswordHash   string   `json:"password_hash"`
	TOTPSecret     string   `json:"totp_secret,omitempty"` // AES-GCM ciphertext, never plaintext on disk.
	RecoveryHashes []string `json:"recovery_hashes,omitempty"`
	NodeID         string   `json:"node_id"`
	NodeName       string   `json:"node_name"`
}

func Valid(username, password string) bool {
	return username != "" && utf8.RuneCountInString(username) <= 64 && !strings.ContainsAny(username, "\x00\r\n") && password != "" && len(password) <= 72
}

func Random(n int) ([]byte, error) { b := make([]byte, n); _, err := rand.Read(b); return b, err }
func Secret() (string, error) {
	b, err := Random(20)
	return base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(b), err
}
func URI(secret, username, node string) string {
	return "otpauth://totp/" + url.PathEscape("FloatLab:"+username+"@"+node) + "?" + url.Values{"secret": {secret}, "issuer": {"FloatLab"}, "algorithm": {"SHA1"}, "digits": {"6"}, "period": {"30"}}.Encode()
}
func Code(secret string, step int64) (string, error) {
	key, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(secret)
	if err != nil || len(key) < 20 {
		return "", fmt.Errorf("invalid TOTP secret")
	}
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(step))
	h := hmac.New(sha1.New, key)
	h.Write(b[:])
	sum := h.Sum(nil)
	offset := sum[len(sum)-1] & 15
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%06d", value%1000000), nil
}
func Verify(secret, code string, now time.Time, last int64) (int64, bool) {
	if len(code) != 6 {
		return 0, false
	}
	step := now.Unix() / 30
	for _, candidate := range []int64{step, step - 1, step + 1} {
		expected, err := Code(secret, candidate)
		if err == nil && candidate > last && subtle.ConstantTimeCompare([]byte(expected), []byte(code)) == 1 {
			return candidate, true
		}
	}
	return 0, false
}
func RecoveryHash(code string) string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.ReplaceAll(strings.TrimSpace(code), "-", ""))))
	return hex.EncodeToString(sum[:])
}
func RecoveryCodes() ([]string, []string, error) {
	codes, hashes := make([]string, 10), make([]string, 10)
	for i := range codes {
		b, err := Random(16)
		if err != nil {
			return nil, nil, err
		}
		codes[i] = hex.EncodeToString(b)
		hashes[i] = RecoveryHash(codes[i])
	}
	return codes, hashes, nil
}
func Encrypt(key []byte, secret string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce, err := Random(aead.NonceSize())
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(aead.Seal(nonce, nonce, []byte(secret), []byte("floatlab-totp-v1"))), nil
}
func Decrypt(key []byte, value string) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	b, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(b) < aead.NonceSize() {
		return "", fmt.Errorf("invalid encrypted secret")
	}
	plain, err := aead.Open(nil, b[:aead.NonceSize()], b[aead.NonceSize():], []byte("floatlab-totp-v1"))
	return string(plain), err
}
func ReadPrivate(path string, value any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("%s must be a private regular file", path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, value)
}

// WritePrivate atomically replaces sensitive local state and syncs it before publishing.
func WritePrivate(path string, value any) error {
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return WriteBytes(path, append(b, '\n'))
}
func WriteBytes(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".floatlab-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
func ValidateBootstrap(b Bootstrap) error {
	if !Valid(b.Username, "placeholder") || b.NodeID == "" || b.NodeName == "" {
		return fmt.Errorf("invalid bootstrap identity")
	}
	if _, err := bcrypt.Cost([]byte(b.PasswordHash)); err != nil {
		return fmt.Errorf("invalid password hash")
	}
	return nil
}
