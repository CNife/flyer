package auth

import (
	"strings"
	"testing"
	"time"
)

func TestPasswordHashVerify(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$") {
		t.Fatalf("unexpected hash format: %s", hash)
	}
	ok, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok {
		t.Fatalf("verify correct password: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword(hash, "wrong")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("wrong password verified")
	}
	if _, err := VerifyPassword("not-a-hash", "x"); err == nil {
		t.Fatal("malformed hash accepted")
	}
}

func TestSignerBindsSlugAndExpiry(t *testing.T) {
	s := NewSigner(strings.Repeat("s", 40))
	now := time.Now().Unix()
	value := s.Sign("abc123", now+3600)
	if !s.Verify(value, "abc123", now) {
		t.Fatal("valid signature rejected")
	}
	if s.Verify(value, "other1", now) {
		t.Fatal("signature accepted for another slug")
	}
	if s.Verify(value, "abc123", now+3601) {
		t.Fatal("expired signature accepted")
	}
	if s.Verify(value+"x", "abc123", now) {
		t.Fatal("tampered signature accepted")
	}
}

func TestSlugAndKeyShapes(t *testing.T) {
	slug, err := NewSlug()
	if err != nil {
		t.Fatal(err)
	}
	if len(slug) != 8 || !ValidSlug(slug) {
		t.Fatalf("unexpected slug %q", slug)
	}
	key, err := NewAPIKey()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(key, "fly_") || len(key) < 40 {
		t.Fatalf("unexpected key %q", key)
	}
	if HashKey(key) == key || len(HashKey(key)) != 64 {
		t.Fatal("key hash is not a sha256 hex digest")
	}
}
