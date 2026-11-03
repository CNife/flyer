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
