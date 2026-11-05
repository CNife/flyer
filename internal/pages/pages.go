// Package pages owns the rules for managing published Pages: who may list,
// expire, or delete one, how a foreign Page is masked as not found, and how a
// Page is presented over the API. The HTTP layer authenticates callers and
// maps error modes to status codes; persistence sits behind Store.
package pages

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/CNife/flyer/internal/api"
	"github.com/CNife/flyer/internal/auth"
	"github.com/CNife/flyer/internal/store"
)

// Sentinel errors are the module's error modes. The HTTP adapter maps them to
// status codes; their text is the user-visible message.
var (
	ErrNotFound = errors.New("page not found")
	ErrExpired  = errors.New("page already expired")
	ErrList     = errors.New("could not list pages")
	ErrExpire   = errors.New("could not expire page")
	ErrDelete   = errors.New("could not delete page")
)

// Store is the persistence adapter behind the seam. *store.Store satisfies it
// in production; tests substitute an in-memory implementation.
type Store interface {
	ListPages(ctx context.Context, ownerKeyID *int64, limit, offset int) ([]store.Page, int64, error)
	GetPage(ctx context.Context, slug string) (*store.Page, error)
	ExpirePage(ctx context.Context, slug string, now time.Time) error
	DeletePage(ctx context.Context, slug string) error
}

// Caller is the authenticated identity acting on Pages. Admin sees and manages
// every Page; a publisher sees and manages only the Pages it owns, so KeyID is
// non-nil for publishers.
type Caller struct {
	Admin bool
	KeyID *int64
}

// Manager applies the Page management rules.
type Manager struct {
	store   Store
	baseURL string
}

// New returns a Manager backed by st. Pages are presented with baseURL.
func New(st Store, baseURL string) *Manager {
	return &Manager{store: st, baseURL: strings.TrimRight(baseURL, "/")}
}

// List returns the Pages visible to caller, newest first, plus the paging
// parameters echoed back to the caller.
func (m *Manager) List(ctx context.Context, caller Caller, limit, offset int) (api.PageListResponse, error) {
	owner := caller.KeyID
	if caller.Admin {
		owner = nil
	}
	pages, total, err := m.store.ListPages(ctx, owner, limit, offset)
	if err != nil {
		return api.PageListResponse{}, fmt.Errorf("%w: %w", ErrList, err)
	}
	out := make([]api.Page, 0, len(pages))
	for _, p := range pages {
		out = append(out, m.wirePage(p))
	}
	return api.PageListResponse{Pages: out, Total: total, Limit: limit, Offset: offset}, nil
}

// Expire makes slug expire at now. A Page the caller does not own is reported
// as not found, so ownership cannot be probed through the API.
func (m *Manager) Expire(ctx context.Context, caller Caller, slug string, now time.Time) (api.ExpirePageResponse, error) {
	if err := m.authorize(ctx, caller, slug); err != nil {
		if errors.Is(err, ErrNotFound) {
			return api.ExpirePageResponse{}, ErrNotFound
		}
		return api.ExpirePageResponse{}, fmt.Errorf("%w: %w", ErrExpire, err)
	}
	if err := m.store.ExpirePage(ctx, slug, now); err != nil {
		switch {
		case errors.Is(err, store.ErrExpired):
			return api.ExpirePageResponse{}, ErrExpired
		case errors.Is(err, store.ErrNotFound):
			return api.ExpirePageResponse{}, ErrNotFound
		default:
			return api.ExpirePageResponse{}, fmt.Errorf("%w: %w", ErrExpire, err)
		}
	}
	return api.ExpirePageResponse{Expired: true, ExpiresAt: time.Unix(now.Unix(), 0)}, nil
}

// Delete removes slug. A Page the caller does not own is reported as not found,
// so ownership cannot be probed through the API.
func (m *Manager) Delete(ctx context.Context, caller Caller, slug string) (api.DeletePageResponse, error) {
	if err := m.authorize(ctx, caller, slug); err != nil {
		if errors.Is(err, ErrNotFound) {
			return api.DeletePageResponse{}, ErrNotFound
		}
		return api.DeletePageResponse{}, fmt.Errorf("%w: %w", ErrDelete, err)
	}
	if err := m.store.DeletePage(ctx, slug); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return api.DeletePageResponse{}, ErrNotFound
		}
		return api.DeletePageResponse{}, fmt.Errorf("%w: %w", ErrDelete, err)
	}
	return api.DeletePageResponse{Deleted: true}, nil
}

// authorize enforces the ownership rule. Every failure the caller can probe —
// malformed slug, missing Page, foreign Page — surfaces as ErrNotFound; store
// failures surface raw so the caller can attach its operation's error mode.
func (m *Manager) authorize(ctx context.Context, caller Caller, slug string) error {
	if !auth.ValidSlug(slug) {
		return ErrNotFound
	}
	page, err := m.store.GetPage(ctx, slug)
	if errors.Is(err, store.ErrNotFound) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !canManage(caller, page) {
		return ErrNotFound
	}
	return nil
}

func canManage(caller Caller, page *store.Page) bool {
	if caller.Admin {
		return true
	}
	return page.OwnerKeyID != nil && caller.KeyID != nil && *page.OwnerKeyID == *caller.KeyID
}

func (m *Manager) wirePage(p store.Page) api.Page {
	return api.Page{
		Slug:       p.Slug,
		Title:      p.Title,
		URL:        m.baseURL + "/" + p.Slug,
		Size:       p.Size,
		Protected:  p.PasswordHash != "",
		OwnerKeyID: p.OwnerKeyID,
		CreatedAt:  p.CreatedAt,
		ExpiresAt:  p.ExpiresAt,
	}
}
