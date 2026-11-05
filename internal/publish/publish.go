// Package publish owns the rules for publishing a Page. It sits behind a small
// interface: the HTTP layer is a thin adapter, and persistence is an adapter
// behind the seam, so the rules are testable without an HTTP round trip.
package publish

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CNife/flyer/internal/auth"
	"github.com/CNife/flyer/internal/store"
)

// Sentinel errors are the module's error modes. The HTTP adapter maps them to
// status codes; their text is the user-visible message and is kept identical
// to the pre-deepening handler's responses.
var (
	ErrEmptyHTML     = errors.New("html must not be empty")
	ErrTooLarge      = errors.New("page is too large")
	ErrInvalidTTL    = errors.New("ttl_seconds must not be negative")
	ErrTTLTooLarge   = errors.New("ttl exceeds server maximum")
	ErrPassword      = errors.New("password is too long")
	ErrSlug          = errors.New("could not generate slug")
	ErrStore         = errors.New("could not store page")
	ErrSlugExhausted = errors.New("could not allocate a slug")
)

const slugAttempts = 8

// Pages is the persistence adapter behind the seam. *store.Store satisfies it
// in production; tests substitute an in-process implementation.
type Pages interface {
	CreatePage(ctx context.Context, p store.Page, html []byte) error
}

// Config holds the server's Page rules.
type Config struct {
	DefaultTTL time.Duration
	MaxTTL     time.Duration
	// MaxHTMLBytes is the maximum accepted HTML size in bytes. It must be
	// positive; a zero value rejects every non-empty Page.
	MaxHTMLBytes int64
}

// Publisher publishes Pages.
type Publisher struct {
	cfg     Config
	pages   Pages
	newSlug func() (string, error)
}

// New returns a Publisher backed by pages. Time is passed per Request so the
// caller's clock stays the single source of time.
func New(cfg Config, pages Pages) *Publisher {
	return &Publisher{cfg: cfg, pages: pages, newSlug: auth.NewSlug}
}

// Request is one Page to publish. Publish validates HTML against Config; the
// caller only transports it.
type Request struct {
	HTML       string
	Title      string
	Password   string
	TTLSeconds int64
	Permanent  bool
	OwnerKeyID *int64
	Now        time.Time
}

// Result is what the caller needs to build the response.
type Result struct {
	Slug      string
	ExpiresAt *time.Time
	Protected bool
}

// Publish applies every Page rule and persists the Page. TTL/permanent rules,
// password hashing, title defaulting and slug allocation all live here and are
// invisible to callers.
func (p *Publisher) Publish(ctx context.Context, req Request) (Result, error) {
	if strings.TrimSpace(req.HTML) == "" {
		return Result{}, ErrEmptyHTML
	}
	if int64(len(req.HTML)) > p.cfg.MaxHTMLBytes {
		return Result{}, ErrTooLarge
	}
	if req.TTLSeconds < 0 {
		return Result{}, ErrInvalidTTL
	}

	var expires *time.Time
	if !req.Permanent {
		ttl := p.cfg.DefaultTTL
		if req.TTLSeconds > 0 {
			ttl = time.Duration(req.TTLSeconds) * time.Second
		}
		if ttl > p.cfg.MaxTTL {
			return Result{}, ErrTTLTooLarge
		}
		e := req.Now.Add(ttl)
		expires = &e
	}

	var passwordHash string
	if req.Password != "" {
		hash, err := auth.HashPassword(req.Password)
		if err != nil {
			return Result{}, fmt.Errorf("%w: %w", ErrPassword, err)
		}
		passwordHash = hash
	}

	for range slugAttempts {
		candidate, err := p.newSlug()
		if err != nil {
			return Result{}, fmt.Errorf("%w: %w", ErrSlug, err)
		}
		title := strings.TrimSpace(req.Title)
		if title == "" {
			title = candidate
		}
		err = p.pages.CreatePage(ctx, store.Page{
			Slug:         candidate,
			Title:        title,
			FileName:     candidate + ".html",
			PasswordHash: passwordHash,
			OwnerKeyID:   req.OwnerKeyID,
			Size:         int64(len(req.HTML)),
			CreatedAt:    req.Now,
			ExpiresAt:    expires,
		}, []byte(req.HTML))
		if errors.Is(err, store.ErrSlugExists) {
			continue
		}
		if err != nil {
			return Result{}, fmt.Errorf("%w: %w", ErrStore, err)
		}
		return Result{Slug: candidate, ExpiresAt: expires, Protected: passwordHash != ""}, nil
	}
	return Result{}, ErrSlugExhausted
}
