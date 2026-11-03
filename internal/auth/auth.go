// Package auth implements password hashing, API key derivation, signed
// password-session cookies, and slug generation.
package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	argonMemory      = 64 * 1024
	argonIterations  = 3
	argonParallelism = 2
	argonSaltLen     = 16
	argonKeyLen      = 32
	SessionCookie    = "flyer_session"
)

var slugPattern = regexp.MustCompile(`^[A-Za-z0-9]{4,32}$`)

func ValidSlug(s string) bool {
	return slugPattern.MatchString(s)
}

// NewSlug returns an 8 character base62 slug.
func NewSlug() (string, error) {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	out := make([]byte, 8)
	buf := make([]byte, 8)
	for i := range out {
		for {
			if _, err := rand.Read(buf[i : i+1]); err != nil {
				return "", err
			}
			// 248 is the largest multiple of 62 below 256; rejecting the
			// top 8 values keeps the distribution uniform.
			if buf[i] < 248 {
				out[i] = alphabet[int(buf[i])%62]
				break
			}
		}
	}
	return string(out), nil
}

func NewAPIKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "fly_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func HashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

func HashPassword(password string) (string, error) {
	if len(password) > 1024 {
		return "", errors.New("password is too long")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	hash := argon2.IDKey([]byte(password), salt, argonIterations, argonMemory, argonParallelism, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonIterations, argonParallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(hash),
	), nil
}

func VerifyPassword(encoded, password string) (bool, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false, errors.New("invalid password hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("unsupported argon2 version")
	}
	var memory, iterations uint32
	var parallelism uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memory, &iterations, &parallelism); err != nil {
		return false, errors.New("invalid argon2 parameters")
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return false, errors.New("invalid argon2 salt")
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false, errors.New("invalid argon2 hash")
	}
	//nolint:gosec // keyLen 取自 want 的长度，受 hash 格式约束为 32 字节
	got := argon2.IDKey([]byte(password), salt, iterations, memory, parallelism, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

type Signer struct {
	secret []byte
}

func NewSigner(secret string) *Signer {
	return &Signer{secret: []byte(secret)}
}

// Sign returns a value binding slug to an expiry unix time.
func (s *Signer) Sign(slug string, exp int64) string {
	payload := slug + "|" + strconv.FormatInt(exp, 10)
	return base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + s.mac(payload)
}

func (s *Signer) Verify(value, slug string, now int64) bool {
	encoded, mac, ok := strings.Cut(value, ".")
	if !ok {
		return false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return false
	}
	expected := s.mac(string(payload))
	if subtle.ConstantTimeCompare([]byte(mac), []byte(expected)) != 1 {
		return false
	}
	gotSlug, expText, ok := strings.Cut(string(payload), "|")
	if !ok || gotSlug != slug {
		return false
	}
	exp, err := strconv.ParseInt(expText, 10, 64)
	if err != nil {
		return false
	}
	return exp > now
}

func (s *Signer) mac(payload string) string {
	h := hmac.New(sha256.New, s.secret)
	h.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}

// CookieExpiry returns the session expiry, capped by the page expiry.
func CookieExpiry(now time.Time, pageExpires *time.Time) time.Time {
	exp := now.Add(time.Hour)
	if pageExpires != nil && pageExpires.Before(exp) {
		exp = *pageExpires
	}
	return exp
}
