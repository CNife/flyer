package store

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func testPage(slug string, now time.Time, expires *time.Time) Page {
	return Page{
		Slug:      slug,
		Title:     "Title " + slug,
		FileName:  slug + ".html",
		Size:      11,
		CreatedAt: now,
		ExpiresAt: expires,
	}
}

func TestPageLifecycle(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	expires := now.Add(time.Hour)
	page := testPage("abc123", now, &expires)

	if err := st.CreatePage(ctx, page, []byte("<h1>hi</h1>")); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetPage(ctx, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != page.Title || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Fatalf("unexpected page: %+v", got)
	}
	f, err := st.OpenPage(got.FileName)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "<h1>hi</h1>" {
		t.Fatalf("unexpected file contents %q", data)
	}

	stats, err := st.Stats(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pages != 1 || stats.Active != 1 || stats.Expired != 0 || stats.Permanent != 0 || stats.Bytes != 11 {
		t.Fatalf("unexpected stats: %+v", stats)
	}

	if err := st.DeletePage(ctx, "abc123"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetPage(ctx, "abc123"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if _, err := st.OpenPage(got.FileName); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("page file still exists: %v", err)
	}
}

func TestDuplicateSlugDoesNotOverwrite(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Now()
	page := testPage("dup123", now, nil)
	if err := st.CreatePage(ctx, page, []byte("first")); err != nil {
		t.Fatal(err)
	}
	err := st.CreatePage(ctx, page, []byte("second"))
	if !errors.Is(err, ErrSlugExists) {
		t.Fatalf("expected duplicate slug error, got %v", err)
	}
	f, err := st.OpenPage("dup123.html")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(f)
	f.Close()
	if string(data) != "first" {
		t.Fatalf("existing file was overwritten: %q", data)
	}
}

func TestCleanupHonorsGrace(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	expired := now.Add(-30 * time.Minute)
	if err := st.CreatePage(ctx, testPage("old123", now, &expired), []byte("x")); err != nil {
		t.Fatal(err)
	}
	n, err := st.Cleanup(ctx, now, time.Hour)
	if err != nil || n != 0 {
		t.Fatalf("cleanup inside grace: n=%d err=%v", n, err)
	}
	n, err = st.Cleanup(ctx, now.Add(2*time.Hour), time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("cleanup after grace: n=%d err=%v", n, err)
	}
	if _, err := st.GetPage(ctx, "old123"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("page was not deleted: %v", err)
	}
	if _, err := st.OpenPage("old123.html"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("page file was not deleted: %v", err)
	}
}

func TestKeys(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	k, err := st.CreateKey(ctx, "agent", "publisher", "hash-one", now)
	if err != nil {
		t.Fatal(err)
	}
	if k.ID == 0 || k.Name != "agent" || k.Role != "publisher" {
		t.Fatalf("unexpected key: %+v", k)
	}
	found, err := st.GetKeyByHash(ctx, "hash-one")
	if err != nil {
		t.Fatal(err)
	}
	if found.ID != k.ID || found.RevokedAt != nil {
		t.Fatalf("unexpected found key: %+v", found)
	}
	keys, err := st.ListKeys(ctx)
	if err != nil || len(keys) != 1 {
		t.Fatalf("list keys: %v %v", keys, err)
	}
	if err := st.RevokeKey(ctx, k.ID, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	found, err = st.GetKeyByHash(ctx, "hash-one")
	if err != nil {
		t.Fatal(err)
	}
	if found.RevokedAt == nil {
		t.Fatal("key not marked revoked")
	}
	if err := st.RevokeKey(ctx, 999, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
}

func TestListPagesByOwner(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	k1, err := st.CreateKey(ctx, "one", "publisher", "hash-one", now)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := st.CreateKey(ctx, "two", "publisher", "hash-two", now)
	if err != nil {
		t.Fatal(err)
	}

	expired := now.Add(-time.Hour)
	pages := []Page{
		{Slug: "aaa111", Title: "old", FileName: "aaa111.html", OwnerKeyID: &k1.ID, Size: 1, CreatedAt: now, ExpiresAt: &expired},
		{Slug: "bbb222", Title: "tie b", FileName: "bbb222.html", OwnerKeyID: &k1.ID, Size: 2, CreatedAt: now.Add(time.Second)},
		{Slug: "ccc333", Title: "tie c", FileName: "ccc333.html", OwnerKeyID: &k1.ID, Size: 3, CreatedAt: now.Add(time.Second)},
		{Slug: "ddd444", Title: "other", FileName: "ddd444.html", OwnerKeyID: &k2.ID, Size: 4, CreatedAt: now.Add(2 * time.Second)},
		{Slug: "eee555", Title: "admin", FileName: "eee555.html", Size: 5, CreatedAt: now.Add(3 * time.Second)},
	}
	for _, p := range pages {
		if err := st.CreatePage(ctx, p, []byte(p.Title)); err != nil {
			t.Fatal(err)
		}
	}

	got, total, err := st.ListPages(ctx, &k1.ID, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(got) != 3 {
		t.Fatalf("owner list: total=%d len=%d", total, len(got))
	}
	want := []string{"ccc333", "bbb222", "aaa111"}
	for i, slug := range want {
		if got[i].Slug != slug {
			t.Fatalf("owner list[%d] = %q, want %q", i, got[i].Slug, slug)
		}
	}
	if got[2].ExpiresAt == nil || !got[2].ExpiresAt.Equal(expired) {
		t.Fatalf("expired page missing expires_at: %+v", got[2])
	}
	if got[1].ExpiresAt != nil {
		t.Fatalf("permanent page has expires_at: %+v", got[1])
	}

	page, total, err := st.ListPages(ctx, &k1.ID, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(page) != 1 || page[0].Slug != "bbb222" {
		t.Fatalf("paged list: total=%d pages=%+v", total, page)
	}

	all, total, err := st.ListPages(ctx, nil, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(all) != 5 || all[0].Slug != "eee555" {
		t.Fatalf("all list: total=%d pages=%+v", total, all)
	}
}

func TestExpirePage(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	key, err := st.CreateKey(ctx, "owner", "publisher", "hash-owner", now)
	if err != nil {
		t.Fatal(err)
	}
	expires := now.Add(time.Hour)
	active := Page{
		Slug: "act123", Title: "active", FileName: "act123.html",
		OwnerKeyID: &key.ID, Size: 1, CreatedAt: now, ExpiresAt: &expires,
	}
	permanent := Page{
		Slug: "per123", Title: "permanent", FileName: "per123.html",
		OwnerKeyID: &key.ID, Size: 1, CreatedAt: now,
	}
	already := Page{
		Slug: "old123", Title: "old", FileName: "old123.html",
		OwnerKeyID: &key.ID, Size: 1, CreatedAt: now, ExpiresAt: &expires,
	}
	for _, p := range []Page{active, permanent, already} {
		if err := st.CreatePage(ctx, p, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}

	if err := st.ExpirePage(ctx, "act123", now); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetPage(ctx, "act123")
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(now) {
		t.Fatalf("active page expiry = %v, want %v", got.ExpiresAt, now)
	}
	if err := st.ExpirePage(ctx, "act123", now); !errors.Is(err, ErrExpired) {
		t.Fatalf("second expire: %v, want ErrExpired", err)
	}

	if err := st.ExpirePage(ctx, "per123", now); err != nil {
		t.Fatal(err)
	}
	got, err = st.GetPage(ctx, "per123")
	if err != nil {
		t.Fatal(err)
	}
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(now) {
		t.Fatalf("permanent page expiry = %v, want %v", got.ExpiresAt, now)
	}

	if err := st.ExpirePage(ctx, "old123", now.Add(2*time.Hour)); !errors.Is(err, ErrExpired) {
		t.Fatalf("expired page expire: %v, want ErrExpired", err)
	}
	if err := st.ExpirePage(ctx, "missing", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing page expire: %v, want ErrNotFound", err)
	}
}
