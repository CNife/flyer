package publish

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CNife/flyer/internal/auth"
	"github.com/CNife/flyer/internal/store"
)

const (
	defaultTTL   = time.Hour
	maxTTL       = 24 * time.Hour
	maxHTMLBytes = 1024
)

var fixedNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

type pageCall struct {
	page store.Page
	html string
}

// fakePages is the in-process adapter behind the seam.
type fakePages struct {
	calls []pageCall
	err   func(call int) error
}

func (f *fakePages) CreatePage(_ context.Context, p store.Page, html []byte) error {
	f.calls = append(f.calls, pageCall{page: p, html: string(html)})
	if f.err != nil {
		return f.err(len(f.calls))
	}
	return nil
}

func testPublisher(t *testing.T, pages Pages) *Publisher {
	t.Helper()
	p := New(Config{DefaultTTL: defaultTTL, MaxTTL: maxTTL, MaxHTMLBytes: maxHTMLBytes}, pages)
	p.newSlug = func() (string, error) { return "abcd1234", nil }
	return p
}

func at(d time.Duration) *time.Time {
	tm := fixedNow.Add(d)
	return &tm
}

func baseRequest() Request {
	return Request{HTML: "<h1>hi</h1>", Now: fixedNow}
}

func TestTTLAndPermanentRules(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Request)
		wantErr error
		wantExp *time.Time
	}{
		{name: "zero or omitted ttl uses default", wantExp: at(defaultTTL)},
		{name: "explicit ttl", mutate: func(r *Request) { r.TTLSeconds = 7200 }, wantExp: at(2 * time.Hour)},
		{name: "ttl at maximum", mutate: func(r *Request) {
			r.TTLSeconds = int64(maxTTL / time.Second)
		}, wantExp: at(maxTTL)},
		{name: "ttl over maximum", mutate: func(r *Request) {
			r.TTLSeconds = int64(maxTTL/time.Second) + 1
		}, wantErr: ErrTTLTooLarge},
		{name: "negative ttl", mutate: func(r *Request) { r.TTLSeconds = -1 }, wantErr: ErrInvalidTTL},
		{name: "permanent has no expiry", mutate: func(r *Request) { r.Permanent = true }},
		{name: "permanent ignores oversized ttl", mutate: func(r *Request) {
			r.Permanent = true
			r.TTLSeconds = int64(maxTTL/time.Second) + 1
		}},
		{name: "permanent still rejects negative ttl", mutate: func(r *Request) {
			r.Permanent = true
			r.TTLSeconds = -1
		}, wantErr: ErrInvalidTTL},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages := &fakePages{}
			req := baseRequest()
			if tc.mutate != nil {
				tc.mutate(&req)
			}
			got, err := testPublisher(t, pages).Publish(context.Background(), req)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if len(pages.calls) != 0 {
					t.Fatalf("persisted %d pages on invalid request", len(pages.calls))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if (got.ExpiresAt == nil) != (tc.wantExp == nil) {
				t.Fatalf("expires = %v, want %v", got.ExpiresAt, tc.wantExp)
			}
			if got.ExpiresAt != nil && !got.ExpiresAt.Equal(*tc.wantExp) {
				t.Fatalf("expires = %v, want %v", *got.ExpiresAt, *tc.wantExp)
			}
			call := pages.calls[0]
			if got.ExpiresAt == nil {
				if call.page.ExpiresAt != nil {
					t.Fatalf("persisted expiry = %v, want nil", call.page.ExpiresAt)
				}
				return
			}
			if call.page.ExpiresAt == nil || !call.page.ExpiresAt.Equal(*got.ExpiresAt) {
				t.Fatalf("persisted expiry = %v, want %v", call.page.ExpiresAt, got.ExpiresAt)
			}
		})
	}
}

func TestHTMLRules(t *testing.T) {
	cases := []struct {
		name    string
		html    string
		wantErr error
	}{
		{name: "empty html", html: "", wantErr: ErrEmptyHTML},
		{name: "blank html", html: " \n\t ", wantErr: ErrEmptyHTML},
		{name: "html at limit", html: strings.Repeat("a", maxHTMLBytes)},
		{name: "html over limit", html: strings.Repeat("a", maxHTMLBytes+1), wantErr: ErrTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages := &fakePages{}
			req := baseRequest()
			req.HTML = tc.html
			_, err := testPublisher(t, pages).Publish(context.Background(), req)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("err = %v, want %v", err, tc.wantErr)
				}
				if len(pages.calls) != 0 {
					t.Fatalf("persisted %d pages on invalid html", len(pages.calls))
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(pages.calls) != 1 || pages.calls[0].html != tc.html {
				t.Fatalf("persisted html = %q", pages.calls[0].html)
			}
		})
	}
}

func TestPasswordRules(t *testing.T) {
	t.Run("empty password skips hashing", func(t *testing.T) {
		pages := &fakePages{}
		got, err := testPublisher(t, pages).Publish(context.Background(), baseRequest())
		if err != nil {
			t.Fatal(err)
		}
		if got.Protected {
			t.Fatal("empty password marked protected")
		}
		if pages.calls[0].page.PasswordHash != "" {
			t.Fatalf("unexpected hash: %q", pages.calls[0].page.PasswordHash)
		}
	})

	t.Run("password is hashed", func(t *testing.T) {
		pages := &fakePages{}
		req := baseRequest()
		req.Password = "secret"
		got, err := testPublisher(t, pages).Publish(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		if !got.Protected {
			t.Fatal("password not marked protected")
		}
		ok, err := auth.VerifyPassword(pages.calls[0].page.PasswordHash, "secret")
		if err != nil || !ok {
			t.Fatalf("hash unusable: ok=%v err=%v", ok, err)
		}
	})

	t.Run("overlong password", func(t *testing.T) {
		pages := &fakePages{}
		req := baseRequest()
		req.Password = strings.Repeat("x", 1025)
		_, err := testPublisher(t, pages).Publish(context.Background(), req)
		if !errors.Is(err, ErrPassword) {
			t.Fatalf("err = %v, want ErrPassword", err)
		}
		if len(pages.calls) != 0 {
			t.Fatal("persisted on invalid password")
		}
	})
}

func TestSlugAllocation(t *testing.T) {
	t.Run("collisions retry", func(t *testing.T) {
		pages := &fakePages{err: func(call int) error {
			if call <= 3 {
				return store.ErrSlugExists
			}
			return nil
		}}
		got, err := testPublisher(t, pages).Publish(context.Background(), baseRequest())
		if err != nil {
			t.Fatal(err)
		}
		if got.Slug != "abcd1234" || len(pages.calls) != 4 {
			t.Fatalf("slug=%q calls=%d", got.Slug, len(pages.calls))
		}
	})

	t.Run("exhaustion after eight collisions", func(t *testing.T) {
		pages := &fakePages{err: func(int) error { return store.ErrSlugExists }}
		_, err := testPublisher(t, pages).Publish(context.Background(), baseRequest())
		if !errors.Is(err, ErrSlugExhausted) {
			t.Fatalf("err = %v, want ErrSlugExhausted", err)
		}
		if len(pages.calls) != slugAttempts {
			t.Fatalf("calls = %d, want %d", len(pages.calls), slugAttempts)
		}
	})

	t.Run("slug generation failure", func(t *testing.T) {
		pages := &fakePages{}
		p := testPublisher(t, pages)
		p.newSlug = func() (string, error) { return "", errors.New("rng unavailable") }
		_, err := p.Publish(context.Background(), baseRequest())
		if !errors.Is(err, ErrSlug) {
			t.Fatalf("err = %v, want ErrSlug", err)
		}
		if len(pages.calls) != 0 {
			t.Fatal("persisted without a slug")
		}
	})

	t.Run("storage failure", func(t *testing.T) {
		pages := &fakePages{err: func(int) error { return errors.New("disk full") }}
		_, err := testPublisher(t, pages).Publish(context.Background(), baseRequest())
		if !errors.Is(err, ErrStore) {
			t.Fatalf("err = %v, want ErrStore", err)
		}
	})
}

func TestPageAssembly(t *testing.T) {
	cases := []struct {
		name      string
		title     string
		wantTitle string
	}{
		{"empty title defaults to slug", "", "abcd1234"},
		{"blank title defaults to slug", "   ", "abcd1234"},
		{"title is trimmed", "  Hello  ", "Hello"},
		{"title preserved", "Hello", "Hello"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pages := &fakePages{}
			req := baseRequest()
			req.Title = tc.title
			req.Permanent = true
			owner := int64(42)
			req.OwnerKeyID = &owner
			_, err := testPublisher(t, pages).Publish(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			call := pages.calls[0]
			if call.page.Title != tc.wantTitle {
				t.Fatalf("title = %q, want %q", call.page.Title, tc.wantTitle)
			}
			if call.page.Slug != "abcd1234" || call.page.FileName != "abcd1234.html" {
				t.Fatalf("slug/file = %q/%q", call.page.Slug, call.page.FileName)
			}
			if call.page.Size != int64(len(req.HTML)) {
				t.Fatalf("size = %d, want %d", call.page.Size, len(req.HTML))
			}
			if !call.page.CreatedAt.Equal(fixedNow) {
				t.Fatalf("created = %v, want %v", call.page.CreatedAt, fixedNow)
			}
			if call.html != req.HTML {
				t.Fatalf("html = %q, want %q", call.html, req.HTML)
			}
			if call.page.OwnerKeyID == nil || *call.page.OwnerKeyID != owner {
				t.Fatal("owner key not carried")
			}
		})
	}
}
