package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/CNife/flyer/internal/api"
)

func TestListOutputAndPagination(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	publishAPI(t, s, key, api.PublishRequest{HTML: "<h1>one</h1>", Title: "First"})
	publishAPI(t, s, key, api.PublishRequest{HTML: "<h1>two</h1>", Title: "Second"})

	out, errOut, err := runCLI(t, s.home, "", "list",
		"--server", s.baseURL, "--key", key, "--json")
	if err != nil {
		t.Fatalf("list --json: %v\nstderr:\n%s", err, errOut)
	}
	var list api.PageListResponse
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("decode list JSON: %v\n%s", err, out)
	}
	if list.Total != 2 || len(list.Pages) != 2 {
		t.Fatalf("list = %+v", list)
	}
	for _, p := range list.Pages {
		if p.URL != s.baseURL+"/"+p.Slug {
			t.Fatalf("page URL = %q", p.URL)
		}
	}

	out, errOut, err = runCLI(t, s.home, "", "list", "--server", s.baseURL, "--key", key)
	if err != nil {
		t.Fatalf("list text: %v\nstderr:\n%s", err, errOut)
	}
	assertContains(t, out, "SLUG")
	assertContains(t, out, "First")
	assertContains(t, out, "Second")
	if errOut != "" {
		t.Fatalf("unexpected list stderr: %q", errOut)
	}

	out, errOut, err = runCLI(t, s.home, "", "list", "--server", s.baseURL, "--key", key,
		"--limit", "1")
	if err != nil {
		t.Fatalf("list --limit 1: %v\nstderr:\n%s", err, errOut)
	}
	if strings.Count(out, "\n") != 2 {
		t.Fatalf("list --limit 1 output:\n%s", out)
	}
	assertContains(t, errOut, "showing 1-1 of 2")
	assertContains(t, errOut, "use --offset 1")

	_, errOut, err = runCLI(t, s.home, "", "list", "--server", s.baseURL, "--key", key,
		"--limit", "1", "--offset", "1")
	if err != nil {
		t.Fatalf("list --offset: %v\nstderr:\n%s", err, errOut)
	}
	assertContains(t, errOut, "showing 2-2 of 2")

	_, errOut, err = runCLI(t, s.home, "", "list", "--server", s.baseURL, "--key", key,
		"--limit", "0")
	if err == nil || !strings.Contains(errOut, "limit must be between 1 and 500") {
		t.Fatalf("limit 0 error = %v, stderr = %q", err, errOut)
	}
	_, errOut, err = runCLI(t, s.home, "", "list", "--server", s.baseURL, "--key", key,
		"--offset", "-1")
	if err == nil || !strings.Contains(errOut, "offset must not be negative") {
		t.Fatalf("offset -1 error = %v, stderr = %q", err, errOut)
	}
}

func TestAuthAndOwnership(t *testing.T) {
	s := newTestServer(t, nil)
	keyA := createPublisher(t, s, "owner-a")
	keyB := createPublisher(t, s, "owner-b")
	pageA := publishAPI(t, s, keyA, api.PublishRequest{HTML: "<h1>a</h1>"})
	pageB := publishAPI(t, s, keyB, api.PublishRequest{HTML: "<h1>b</h1>"})

	_, errOut, err := runCLI(t, s.home, "", "list", "--server", s.baseURL)
	if err == nil || !strings.Contains(errOut, "no API key configured") {
		t.Fatalf("list without key error = %v, stderr = %q", err, errOut)
	}
	_, errOut, err = runCLI(t, s.home, "", "list", "--server", s.baseURL, "--key", "fly_invalid")
	if err == nil || !strings.Contains(errOut, "401") {
		t.Fatalf("list with invalid key error = %v, stderr = %q", err, errOut)
	}

	assertListedSlugs(t, s, keyA, pageA.Slug)
	assertListedSlugs(t, s, keyB, pageB.Slug)
	assertListedSlugs(t, s, s.adminKey, pageA.Slug, pageB.Slug)

	_, errOut, err = runCLI(t, s.home, "", "expire", pageB.Slug,
		"--server", s.baseURL, "--key", keyA)
	if err == nil || !strings.Contains(errOut, "page not found") {
		t.Fatalf("foreign expire error = %v, stderr = %q", err, errOut)
	}
	_, errOut, err = runCLI(t, s.home, "", "delete", pageB.Slug,
		"--server", s.baseURL, "--key", keyA)
	if err == nil || !strings.Contains(errOut, "page not found") {
		t.Fatalf("foreign delete error = %v, stderr = %q", err, errOut)
	}

	_, errOut, err = runCLI(t, s.home, "", "expire", pageB.Slug,
		"--server", s.baseURL, "--key", s.adminKey)
	if err != nil {
		t.Fatalf("admin expire: %v\nstderr:\n%s", err, errOut)
	}
	status, _, body := getPage(t, s, pageB.Slug, nil)
	assertStatus(t, status, http.StatusGone, body)

	_, errOut, err = runCLI(t, s.home, "", "delete", pageA.Slug,
		"--server", s.baseURL, "--key", s.adminKey)
	if err != nil {
		t.Fatalf("admin delete: %v\nstderr:\n%s", err, errOut)
	}
	status, _, body = getPage(t, s, pageA.Slug, nil)
	assertStatus(t, status, http.StatusNotFound, body)

	_, errOut, err = runCLI(t, s.home, "", "admin", "stats",
		"--server", s.baseURL, "--admin-key", keyA)
	if err == nil || !strings.Contains(errOut, "admin key required") {
		t.Fatalf("publisher admin stats error = %v, stderr = %q", err, errOut)
	}
}

func TestStatsAndKeys(t *testing.T) {
	s := newTestServer(t, nil)

	out, errOut, err := runCLI(t, s.home, "", "admin", "stats",
		"--server", s.baseURL, "--admin-key", s.adminKey, "--json")
	if err != nil {
		t.Fatalf("initial stats: %v\nstderr:\n%s", err, errOut)
	}
	var stats api.StatsResponse
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		t.Fatalf("decode stats: %v\n%s", err, out)
	}
	if stats.Pages != 0 || stats.Keys != 0 {
		t.Fatalf("initial stats = %+v", stats)
	}

	out, errOut, err = runCLI(t, s.home, "", "admin", "key", "create",
		"--server", s.baseURL, "--admin-key", s.adminKey,
		"--name", "agent", "--json")
	if err != nil {
		t.Fatalf("create key: %v\nstderr:\n%s", err, errOut)
	}
	var created api.CreateKeyResponse
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("decode create key: %v\n%s", err, out)
	}
	if created.ID <= 0 || created.Name != "agent" || created.Role != "publisher" {
		t.Fatalf("created key = %+v", created)
	}

	out, errOut, err = runCLI(t, s.home, "", "admin", "key", "list",
		"--server", s.baseURL, "--admin-key", s.adminKey, "--json")
	if err != nil {
		t.Fatalf("list keys: %v\nstderr:\n%s", err, errOut)
	}
	var keys api.KeyListResponse
	if err := json.Unmarshal([]byte(out), &keys); err != nil {
		t.Fatalf("decode keys: %v\n%s", err, out)
	}
	if len(keys.Keys) != 1 || keys.Keys[0].ID != created.ID || keys.Keys[0].RevokedAt != nil {
		t.Fatalf("keys = %+v", keys)
	}

	publishAPI(t, s, created.Key, api.PublishRequest{HTML: "<h1>stats</h1>"})
	out, errOut, err = runCLI(t, s.home, "", "admin", "stats",
		"--server", s.baseURL, "--admin-key", s.adminKey, "--json")
	if err != nil {
		t.Fatalf("stats after publish: %v\nstderr:\n%s", err, errOut)
	}
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		t.Fatalf("decode stats: %v\n%s", err, out)
	}
	if stats.Pages != 1 || stats.Active != 1 || stats.Keys != 1 {
		t.Fatalf("stats after publish = %+v", stats)
	}

	_, errOut, err = runCLI(t, s.home, "", "admin", "key", "revoke", fmt.Sprint(created.ID),
		"--server", s.baseURL, "--admin-key", s.adminKey)
	if err != nil {
		t.Fatalf("revoke key: %v\nstderr:\n%s", err, errOut)
	}
	_, errOut, err = runCLI(t, s.home, "", "list",
		"--server", s.baseURL, "--key", created.Key)
	if err == nil || !strings.Contains(errOut, "401") {
		t.Fatalf("revoked key list error = %v, stderr = %q", err, errOut)
	}

	out, errOut, err = runCLI(t, s.home, "", "admin", "stats",
		"--server", s.baseURL, "--admin-key", s.adminKey, "--json")
	if err != nil {
		t.Fatalf("stats after revoke: %v\nstderr:\n%s", err, errOut)
	}
	if err := json.Unmarshal([]byte(out), &stats); err != nil {
		t.Fatalf("decode stats: %v\n%s", err, out)
	}
	if stats.Keys != 0 {
		t.Fatalf("stats.Keys after revoke = %d", stats.Keys)
	}

	_, errOut, err = runCLI(t, s.home, "", "admin", "key", "create",
		"--server", s.baseURL, "--admin-key", s.adminKey)
	if err == nil || !strings.Contains(errOut, "--name is required") {
		t.Fatalf("missing key name error = %v, stderr = %q", err, errOut)
	}
	_, errOut, err = runCLI(t, s.home, "", "admin", "key", "create",
		"--server", s.baseURL, "--admin-key", s.adminKey,
		"--name", "bad", "--role", "admin")
	if err == nil || !strings.Contains(errOut, "role must be publisher") {
		t.Fatalf("bad role error = %v, stderr = %q", err, errOut)
	}
}

func TestConfigCommands(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	home := t.TempDir()

	out, errOut, err := runCLI(t, home, "", "config", "path")
	if err != nil {
		t.Fatalf("config path: %v\nstderr:\n%s", err, errOut)
	}
	configPath := strings.TrimSpace(out)
	if !strings.Contains(configPath, "flyer") || !strings.HasSuffix(configPath, "config.json") {
		t.Fatalf("config path = %q", configPath)
	}

	for _, args := range [][]string{
		{"config", "set", "server", s.baseURL},
		{"config", "set", "key", key},
		{"config", "set", "admin-key", s.adminKey},
	} {
		if _, errOut, err := runCLI(t, home, "", args...); err != nil {
			t.Fatalf("%v: %v\nstderr:\n%s", args, err, errOut)
		}
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(configPath)
		if err != nil {
			t.Fatalf("stat config: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("config mode = %o, want 600", mode)
		}
	}

	out, errOut, err = runCLI(t, home, "", "config", "show")
	if err != nil {
		t.Fatalf("config show: %v\nstderr:\n%s", err, errOut)
	}
	assertContains(t, out, "server: "+s.baseURL)
	assertContains(t, out, "key: "+mask(key))
	assertContains(t, out, "admin_key: "+mask(s.adminKey))
	if strings.Contains(out, key) || strings.Contains(out, s.adminKey) {
		t.Fatalf("config show leaked a full key:\n%s", out)
	}

	path := writeHTML(t, t.TempDir(), "configured.html", "<h1>configured</h1>")
	if _, errOut, err := runCLI(t, home, "", "publish", path, "--no-qr"); err != nil {
		t.Fatalf("publish via config: %v\nstderr:\n%s", err, errOut)
	}

	// FLYER_SERVER overrides the config server.
	if _, errOut, err := runCLI(t, home, "", "config", "set", "server", "http://127.0.0.1:1"); err != nil {
		t.Fatalf("set invalid server: %v\nstderr:\n%s", err, errOut)
	}
	if _, errOut, err := runCLIEnv(t, home, []string{"FLYER_SERVER=" + s.baseURL}, "",
		"publish", path, "--no-qr"); err != nil {
		t.Fatalf("publish with FLYER_SERVER: %v\nstderr:\n%s", err, errOut)
	}

	// FLYER_KEY overrides the config key; an explicit --key overrides the env.
	if _, errOut, err := runCLIEnv(t, home, []string{"FLYER_KEY=fly_invalid"}, "",
		"publish", path, "--server", s.baseURL, "--no-qr"); err == nil {
		t.Fatalf("invalid FLYER_KEY succeeded:\n%s", errOut)
	}
	if _, errOut, err := runCLIEnv(t, home, []string{"FLYER_KEY=fly_invalid"}, "",
		"publish", path, "--server", s.baseURL, "--key", key, "--no-qr"); err != nil {
		t.Fatalf("--key should override FLYER_KEY: %v\nstderr:\n%s", err, errOut)
	}

	_, errOut, err = runCLI(t, home, "", "config", "set", "server", "not-a-url")
	if err == nil || !strings.Contains(errOut, "absolute http(s) URL") {
		t.Fatalf("invalid config server error = %v, stderr = %q", err, errOut)
	}
}

func TestOpenCommand(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	page := publishAPI(t, s, key, api.PublishRequest{HTML: "<h1>open</h1>"})

	_, errOut, err := runCLI(t, s.home, "", "open")
	if err == nil || !strings.Contains(errOut, "usage: flyer open") {
		t.Fatalf("open without slug error = %v, stderr = %q", err, errOut)
	}
	_, errOut, err = runCLI(t, s.home, "", "open", "one", "two")
	if err == nil || !strings.Contains(errOut, "usage: flyer open") {
		t.Fatalf("open with two slugs error = %v, stderr = %q", err, errOut)
	}

	if runtime.GOOS != "windows" {
		stubDir := t.TempDir()
		name := "xdg-open"
		if runtime.GOOS == "darwin" {
			name = "open"
		}
		stub := filepath.Join(stubDir, name)
		script := "#!/bin/sh\nprintf '%s\\n' \"$1\" > \"$FLYER_OPEN_OUT\"\n"
		if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
			t.Fatalf("write browser stub: %v", err)
		}
		openOut := filepath.Join(t.TempDir(), "opened")
		env := []string{
			"FLYER_OPEN_OUT=" + openOut,
			"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		}
		_, errOut, err = runCLIEnv(t, s.home, env, "",
			"open", page.Slug, "--server", s.baseURL)
		if err != nil {
			t.Fatalf("open: %v\nstderr:\n%s", err, errOut)
		}
		if got := waitForFile(t, openOut); got != page.URL {
			t.Fatalf("browser opened %q, want %q", got, page.URL)
		}

		expired := publishAPI(t, s, key, api.PublishRequest{HTML: "<h1>expired</h1>", Permanent: true})
		if _, errOut, err := runCLI(t, s.home, "", "expire", expired.Slug,
			"--server", s.baseURL, "--key", key); err != nil {
			t.Fatalf("expire before open: %v\nstderr:\n%s", err, errOut)
		}
		expiredOut := filepath.Join(t.TempDir(), "expired-opened")
		_, errOut, err = runCLIEnv(t, s.home, []string{
			"FLYER_OPEN_OUT=" + expiredOut,
			"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		}, "", "open", expired.Slug, "--server", s.baseURL)
		if err == nil || !strings.Contains(errOut, "page has expired") {
			t.Fatalf("open expired error = %v, stderr = %q", err, errOut)
		}
		if _, err := os.Stat(expiredOut); !os.IsNotExist(err) {
			t.Fatalf("browser ran for an expired page: %v", err)
		}

		password := publishAPI(t, s, key, api.PublishRequest{HTML: "<h1>locked</h1>", Password: "secret"})
		passwordOut := filepath.Join(t.TempDir(), "password-opened")
		_, errOut, err = runCLIEnv(t, s.home, []string{
			"FLYER_OPEN_OUT=" + passwordOut,
			"PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH"),
		}, "", "open", password.Slug, "--server", s.baseURL)
		if err != nil {
			t.Fatalf("open password page: %v\nstderr:\n%s", err, errOut)
		}
		if got := waitForFile(t, passwordOut); got != password.URL {
			t.Fatalf("browser opened %q, want %q", got, password.URL)
		}
	}

	_, errOut, err = runCLI(t, s.home, "", "open", "ZZZZZZZZ", "--server", s.baseURL)
	if err == nil || !strings.Contains(errOut, "page not found") {
		t.Fatalf("open missing error = %v, stderr = %q", err, errOut)
	}
}

func TestServerUnavailable(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	s.stop(t)

	_, errOut, err := runCLI(t, s.home, "", "list", "--server", s.baseURL, "--key", key)
	if err == nil {
		t.Fatalf("list against a stopped server succeeded:\n%s", errOut)
	}
	if errOut == "" {
		t.Fatal("CLI printed no error for a stopped server")
	}
}

func TestConcurrentPublish(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	path := writeHTML(t, t.TempDir(), "concurrent.html", "<h1>concurrent</h1>")

	const workers = 8
	slugs := make(chan string, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stdout, stderr, err := runCLIRaw(s.home, nil, "", "publish", path,
				"--server", s.baseURL, "--key", key, "--no-qr")
			if err != nil {
				errs <- fmt.Errorf("%w: %s", err, stderr)
				return
			}
			u, err := url.Parse(strings.TrimSpace(stdout))
			if err != nil || u.Path == "" || u.Path == "/" {
				errs <- fmt.Errorf("bad URL %q", stdout)
				return
			}
			slugs <- strings.TrimPrefix(u.Path, "/")
		}()
	}
	wg.Wait()
	close(errs)
	close(slugs)
	for err := range errs {
		t.Fatalf("concurrent publish: %v", err)
	}

	seen := map[string]bool{}
	for slug := range slugs {
		if slug == "" || seen[slug] {
			t.Fatalf("duplicate or empty slug %q", slug)
		}
		seen[slug] = true
	}
	if len(seen) != workers {
		t.Fatalf("published %d unique slugs, want %d", len(seen), workers)
	}

	status, _, body := apiRequest(t, s, http.MethodGet, "/api/pages?limit=100", key, nil)
	assertStatus(t, status, http.StatusOK, body)
	var list api.PageListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v\n%s", err, body)
	}
	if list.Total != workers || len(list.Pages) != workers {
		t.Fatalf("list total = %d pages = %d, want %d", list.Total, len(list.Pages), workers)
	}
	for _, p := range list.Pages {
		if !seen[p.Slug] {
			t.Fatalf("unexpected slug %q", p.Slug)
		}
	}
}

func assertListedSlugs(t *testing.T, s *testServer, key string, want ...string) {
	t.Helper()
	status, _, body := apiRequest(t, s, http.MethodGet, "/api/pages", key, nil)
	assertStatus(t, status, http.StatusOK, body)
	var list api.PageListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v\n%s", err, body)
	}
	if int(list.Total) != len(want) || len(list.Pages) != len(want) {
		t.Fatalf("list = %+v, want %v", list, want)
	}
	got := map[string]bool{}
	for _, p := range list.Pages {
		got[p.Slug] = true
	}
	for _, slug := range want {
		if !got[slug] {
			t.Fatalf("list missing slug %q: %+v", slug, list)
		}
	}
}

func mask(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 12 {
		return key[:4] + "..."
	}
	return key[:8] + "..." + key[len(key)-4:]
}
