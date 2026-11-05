package pages

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/CNife/flyer/internal/api"
	"github.com/CNife/flyer/internal/store"
)

const testBaseURL = "http://example.test"

var testNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// fakeStore is the in-memory adapter behind the seam.
type fakeStore struct {
	pages       map[string]store.Page
	listCalled  bool
	listOwner   *int64
	getCalled   bool
	expireCalls []string
	deleteCalls []string
	listErr     error
	getErr      error
	expireErr   error
	deleteErr   error
}

func newFakeStore(pages ...store.Page) *fakeStore {
	f := &fakeStore{pages: make(map[string]store.Page, len(pages))}
	for _, p := range pages {
		f.pages[p.Slug] = p
	}
	return f
}

func (f *fakeStore) ListPages(_ context.Context, ownerKeyID *int64, limit, offset int) ([]store.Page, int64, error) {
	f.listCalled = true
	f.listOwner = ownerKeyID
	if f.listErr != nil {
		return nil, 0, f.listErr
	}
	var out []store.Page
	for _, p := range f.pages {
		if ownerKeyID != nil && (p.OwnerKeyID == nil || *p.OwnerKeyID != *ownerKeyID) {
			continue
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].Slug > out[j].Slug
	})
	total := int64(len(out))
	if offset >= len(out) {
		return nil, total, nil
	}
	out = out[offset:]
	if limit < len(out) {
		out = out[:limit]
	}
	return out, total, nil
}

func (f *fakeStore) GetPage(_ context.Context, slug string) (*store.Page, error) {
	f.getCalled = true
	if f.getErr != nil {
		return nil, f.getErr
	}
	p, ok := f.pages[slug]
	if !ok {
		return nil, store.ErrNotFound
	}
	return &p, nil
}

func (f *fakeStore) ExpirePage(_ context.Context, slug string, _ time.Time) error {
	f.expireCalls = append(f.expireCalls, slug)
	return f.expireErr
}

func (f *fakeStore) DeletePage(_ context.Context, slug string) error {
	f.deleteCalls = append(f.deleteCalls, slug)
	return f.deleteErr
}

func seedPages() *fakeStore {
	expires := testNow.Add(time.Hour)
	return newFakeStore(
		store.Page{
			Slug: "mine1234", Title: "Mine", FileName: "mine1234.html",
			PasswordHash: "hash", OwnerKeyID: new(int64(1)), Size: 10, CreatedAt: testNow,
			ExpiresAt: &expires,
		},
		store.Page{
			Slug: "other123", Title: "Other", FileName: "other123.html",
			OwnerKeyID: new(int64(2)), Size: 20, CreatedAt: testNow.Add(time.Second),
		},
		store.Page{
			Slug: "admin123", Title: "Admin", FileName: "admin123.html",
			Size: 30, CreatedAt: testNow.Add(2 * time.Second),
		},
	)
}

func testManager() (*Manager, *fakeStore) {
	st := seedPages()
	return New(st, testBaseURL), st
}

func TestListVisibility(t *testing.T) {
	m, st := testManager()

	t.Run("publisher sees only owned pages", func(t *testing.T) {
		got, err := m.List(context.Background(), Caller{KeyID: new(int64(1))}, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !st.listCalled || st.listOwner == nil || *st.listOwner != 1 {
			t.Fatalf("owner filter not passed: called=%v owner=%v", st.listCalled, st.listOwner)
		}
		if got.Total != 1 || len(got.Pages) != 1 || got.Pages[0].Slug != "mine1234" {
			t.Fatalf("unexpected list: %+v", got)
		}
		if got.Limit != 10 || got.Offset != 0 {
			t.Fatalf("paging not echoed: %+v", got)
		}
	})

	t.Run("admin sees every page", func(t *testing.T) {
		got, err := m.List(context.Background(), Caller{Admin: true}, 10, 0)
		if err != nil {
			t.Fatal(err)
		}
		if st.listOwner != nil {
			t.Fatalf("admin owner filter = %v, want nil", *st.listOwner)
		}
		if got.Total != 3 || len(got.Pages) != 3 {
			t.Fatalf("unexpected admin list: %+v", got)
		}
	})
}

func TestListMapsPagesToWireTypes(t *testing.T) {
	m, _ := testManager()
	got, err := m.List(context.Background(), Caller{KeyID: new(int64(1))}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	p := got.Pages[0]
	if p.URL != testBaseURL+"/mine1234" {
		t.Fatalf("url = %q", p.URL)
	}
	if !p.Protected {
		t.Fatal("password-protected page not marked protected")
	}
	if p.OwnerKeyID == nil || *p.OwnerKeyID != 1 {
		t.Fatalf("owner = %v", p.OwnerKeyID)
	}
	if p.Title != "Mine" || p.Size != 10 || !p.CreatedAt.Equal(testNow) || p.ExpiresAt == nil {
		t.Fatalf("unexpected page: %+v", p)
	}

	all, err := m.List(context.Background(), Caller{Admin: true}, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var adminPage *api.Page
	for i := range all.Pages {
		if all.Pages[i].Slug == "admin123" {
			adminPage = &all.Pages[i]
		}
	}
	if adminPage == nil || adminPage.OwnerKeyID != nil {
		t.Fatalf("admin page owner = %+v", adminPage)
	}
}

func TestListStoreError(t *testing.T) {
	m, st := testManager()
	st.listErr = errors.New("boom")
	if _, err := m.List(context.Background(), Caller{Admin: true}, 10, 0); !errors.Is(err, ErrList) {
		t.Fatalf("err = %v, want ErrList", err)
	}
}

func TestExpirePage(t *testing.T) {
	m, st := testManager()
	got, err := m.Expire(context.Background(), Caller{KeyID: new(int64(1))}, "mine1234", testNow)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Expired || !got.ExpiresAt.Equal(testNow) {
		t.Fatalf("unexpected response: %+v", got)
	}
	if len(st.expireCalls) != 1 || st.expireCalls[0] != "mine1234" {
		t.Fatalf("expire calls = %v", st.expireCalls)
	}
}

func TestExpireAdminCanManageAnyPage(t *testing.T) {
	m, st := testManager()
	if _, err := m.Expire(context.Background(), Caller{Admin: true}, "mine1234", testNow); err != nil {
		t.Fatal(err)
	}
	if len(st.expireCalls) != 1 {
		t.Fatalf("expire calls = %v", st.expireCalls)
	}
}

func TestExpireMasksForeignPageAsNotFound(t *testing.T) {
	m, st := testManager()
	if _, err := m.Expire(context.Background(), Caller{KeyID: new(int64(2))}, "mine1234", testNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(st.expireCalls) != 0 {
		t.Fatalf("foreign page expired: %v", st.expireCalls)
	}
}

func TestExpireUnknownOrInvalidSlug(t *testing.T) {
	m, st := testManager()
	if _, err := m.Expire(context.Background(), Caller{KeyID: new(int64(1))}, "nope1234", testNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown slug err = %v", err)
	}
	st.getCalled = false
	if _, err := m.Expire(context.Background(), Caller{KeyID: new(int64(1))}, "zz", testNow); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid slug err = %v", err)
	}
	if st.getCalled {
		t.Fatal("store queried for invalid slug")
	}
}

func TestExpireStoreErrorModes(t *testing.T) {
	t.Run("already expired", func(t *testing.T) {
		m, st := testManager()
		st.expireErr = store.ErrExpired
		if _, err := m.Expire(context.Background(), Caller{KeyID: new(int64(1))}, "mine1234", testNow); !errors.Is(err, ErrExpired) {
			t.Fatalf("err = %v, want ErrExpired", err)
		}
	})

	t.Run("read failure", func(t *testing.T) {
		m, st := testManager()
		st.getErr = errors.New("boom")
		if _, err := m.Expire(context.Background(), Caller{KeyID: new(int64(1))}, "mine1234", testNow); !errors.Is(err, ErrExpire) {
			t.Fatalf("err = %v, want ErrExpire", err)
		}
	})

	t.Run("write failure", func(t *testing.T) {
		m, st := testManager()
		st.expireErr = errors.New("boom")
		if _, err := m.Expire(context.Background(), Caller{KeyID: new(int64(1))}, "mine1234", testNow); !errors.Is(err, ErrExpire) {
			t.Fatalf("err = %v, want ErrExpire", err)
		}
	})
}

func TestDeletePage(t *testing.T) {
	m, st := testManager()
	got, err := m.Delete(context.Background(), Caller{KeyID: new(int64(1))}, "mine1234")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Deleted {
		t.Fatalf("unexpected response: %+v", got)
	}
	if len(st.deleteCalls) != 1 || st.deleteCalls[0] != "mine1234" {
		t.Fatalf("delete calls = %v", st.deleteCalls)
	}
}

func TestDeleteAdminCanManageAnyPage(t *testing.T) {
	m, st := testManager()
	if _, err := m.Delete(context.Background(), Caller{Admin: true}, "mine1234"); err != nil {
		t.Fatal(err)
	}
	if len(st.deleteCalls) != 1 {
		t.Fatalf("delete calls = %v", st.deleteCalls)
	}
}

func TestDeleteMasksForeignPageAsNotFound(t *testing.T) {
	m, st := testManager()
	if _, err := m.Delete(context.Background(), Caller{KeyID: new(int64(2))}, "mine1234"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if len(st.deleteCalls) != 0 {
		t.Fatalf("foreign page deleted: %v", st.deleteCalls)
	}
}

func TestDeleteUnknownOrInvalidSlug(t *testing.T) {
	m, st := testManager()
	if _, err := m.Delete(context.Background(), Caller{KeyID: new(int64(1))}, "nope1234"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown slug err = %v", err)
	}
	st.getCalled = false
	if _, err := m.Delete(context.Background(), Caller{KeyID: new(int64(1))}, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("invalid slug err = %v", err)
	}
	if st.getCalled {
		t.Fatal("store queried for invalid slug")
	}
}

func TestDeleteStoreErrorModes(t *testing.T) {
	t.Run("vanished page", func(t *testing.T) {
		m, st := testManager()
		st.deleteErr = store.ErrNotFound
		if _, err := m.Delete(context.Background(), Caller{KeyID: new(int64(1))}, "mine1234"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
	})

	t.Run("read failure", func(t *testing.T) {
		m, st := testManager()
		st.getErr = errors.New("boom")
		if _, err := m.Delete(context.Background(), Caller{KeyID: new(int64(1))}, "mine1234"); !errors.Is(err, ErrDelete) {
			t.Fatalf("err = %v, want ErrDelete", err)
		}
	})

	t.Run("write failure", func(t *testing.T) {
		m, st := testManager()
		st.deleteErr = errors.New("boom")
		if _, err := m.Delete(context.Background(), Caller{KeyID: new(int64(1))}, "mine1234"); !errors.Is(err, ErrDelete) {
			t.Fatalf("err = %v, want ErrDelete", err)
		}
	})
}
