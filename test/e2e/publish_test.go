package e2e

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CNife/flyer/internal/api"
)

func TestPublishFileStdoutAndPage(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	path := writeHTML(t, t.TempDir(), "report.html", "<h1>hello flyer</h1>")

	out, errOut, err := runCLI(t, s.home, "", "publish", path,
		"--server", s.baseURL, "--key", key, "--no-qr")
	if err != nil {
		t.Fatalf("publish: %v\nstderr:\n%s", err, errOut)
	}
	if got := strings.TrimSpace(out); got != s.baseURL+"/"+slugFromURL(t, got) {
		t.Fatalf("stdout = %q", out)
	}
	if strings.Count(out, "\n") != 1 {
		t.Fatalf("stdout should contain one URL line: %q", out)
	}
	slug := slugFromURL(t, strings.TrimSpace(out))
	for _, want := range []string{"slug: " + slug, "protected: no", "expires:"} {
		assertContains(t, errOut, want)
	}
	if strings.Contains(errOut, "█") || strings.Contains(errOut, "▀") {
		t.Fatalf("QR code written despite --no-qr:\n%s", errOut)
	}

	status, header, body := getPage(t, s, slug, nil)
	assertStatus(t, status, http.StatusOK, body)
	assertContains(t, string(body), "hello flyer")
	if got := header.Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Fatalf("X-Robots-Tag = %q", got)
	}
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q", got)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
}

func TestPublishJSON(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	path := writeHTML(t, t.TempDir(), "json.html", "<h1>json</h1>")

	out, errOut, err := runCLI(t, s.home, "", "publish", path,
		"--server", s.baseURL, "--key", key, "--json")
	if err != nil {
		t.Fatalf("publish --json: %v\nstderr:\n%s", err, errOut)
	}
	if errOut != "" {
		t.Fatalf("--json wrote to stderr: %q", errOut)
	}
	var resp api.PublishResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode publish JSON: %v\n%s", err, out)
	}
	if resp.Slug == "" || resp.URL != s.baseURL+"/"+resp.Slug {
		t.Fatalf("publish response = %+v", resp)
	}
	if resp.ExpiresAt == nil {
		t.Fatal("default publish has no expiry")
	}
	if resp.Protected {
		t.Fatal("unprotected publish reported protected")
	}
	status, _, body := getPage(t, s, resp.Slug, nil)
	assertStatus(t, status, http.StatusOK, body)
}

func TestPublishStdinAndTitle(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")

	out, errOut, err := runCLI(t, s.home, "<h1>from stdin</h1>",
		"publish", "-", "--server", s.baseURL, "--key", key,
		"--title", "Stdin Page", "--json")
	if err != nil {
		t.Fatalf("publish stdin: %v\nstderr:\n%s", err, errOut)
	}
	var resp api.PublishResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode publish JSON: %v\n%s", err, out)
	}
	status, _, body := apiRequest(t, s, http.MethodGet, "/api/pages", key, nil)
	assertStatus(t, status, http.StatusOK, body)
	var list api.PageListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v\n%s", err, body)
	}
	if len(list.Pages) != 1 || list.Pages[0].Title != "Stdin Page" {
		t.Fatalf("list = %+v", list)
	}
}

func TestPublishPermanent(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	path := writeHTML(t, t.TempDir(), "permanent.html", "<h1>forever</h1>")

	out, errOut, err := runCLI(t, s.home, "", "publish", path,
		"--server", s.baseURL, "--key", key, "--permanent", "--json")
	if err != nil {
		t.Fatalf("publish permanent: %v\nstderr:\n%s", err, errOut)
	}
	var resp api.PublishResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode publish JSON: %v\n%s", err, out)
	}
	if resp.ExpiresAt != nil {
		t.Fatalf("permanent page expires at %v", resp.ExpiresAt)
	}

	status, _, body := apiRequest(t, s, http.MethodGet, "/api/pages", key, nil)
	assertStatus(t, status, http.StatusOK, body)
	var list api.PageListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v\n%s", err, body)
	}
	if len(list.Pages) != 1 || list.Pages[0].ExpiresAt != nil {
		t.Fatalf("permanent page in list = %+v", list.Pages)
	}
}

func TestPublishTTL(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	path := writeHTML(t, t.TempDir(), "ttl.html", "<h1>ttl</h1>")

	out, errOut, err := runCLI(t, s.home, "", "publish", path,
		"--server", s.baseURL, "--key", key, "--ttl", "1h", "--json")
	if err != nil {
		t.Fatalf("publish ttl: %v\nstderr:\n%s", err, errOut)
	}
	var resp api.PublishResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode publish JSON: %v\n%s", err, out)
	}
	if resp.ExpiresAt == nil {
		t.Fatal("--ttl response has no expiry")
	}
	remaining := time.Until(*resp.ExpiresAt)
	if remaining < 59*time.Minute || remaining > 61*time.Minute {
		t.Fatalf("expiry remaining = %v, want about 1h", remaining)
	}
}

func TestPublishPassword(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	path := writeHTML(t, t.TempDir(), "secret.html", "<h1>secret content</h1>")

	out, errOut, err := runCLI(t, s.home, "", "publish", path,
		"--server", s.baseURL, "--key", key, "--password", "correct horse", "--json")
	if err != nil {
		t.Fatalf("publish password: %v\nstderr:\n%s", err, errOut)
	}
	var resp api.PublishResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode publish JSON: %v\n%s", err, out)
	}
	if !resp.Protected {
		t.Fatal("password page not marked protected")
	}

	status, _, body := getPage(t, s, resp.Slug, nil)
	assertStatus(t, status, http.StatusUnauthorized, body)
	assertContains(t, string(body), `type="password"`)

	status, body = postPassword(t, s, resp.Slug, "wrong", newCookieJar(t))
	assertStatus(t, status, http.StatusUnauthorized, body)
	assertContains(t, string(body), "Wrong password")

	jar := newCookieJar(t)
	status, body = postPassword(t, s, resp.Slug, "correct horse", jar)
	assertStatus(t, status, http.StatusOK, body)
	assertContains(t, string(body), "secret content")
	status, _, body = getPage(t, s, resp.Slug, jar)
	assertStatus(t, status, http.StatusOK, body)
	assertContains(t, string(body), "secret content")

	// FLYER_PASSWORD is used when --password is absent.
	envPath := writeHTML(t, t.TempDir(), "env-secret.html", "<h1>env secret</h1>")
	out, errOut, err = runCLIEnv(t, s.home, []string{"FLYER_PASSWORD=from-env"}, "",
		"publish", envPath, "--server", s.baseURL, "--key", key, "--json")
	if err != nil {
		t.Fatalf("publish with FLYER_PASSWORD: %v\nstderr:\n%s", err, errOut)
	}
	var envResp api.PublishResponse
	if err := json.Unmarshal([]byte(out), &envResp); err != nil {
		t.Fatalf("decode publish JSON: %v\n%s", err, out)
	}
	if !envResp.Protected {
		t.Fatal("FLYER_PASSWORD did not protect the page")
	}
	status, _, _ = getPage(t, s, envResp.Slug, nil)
	assertStatus(t, status, http.StatusUnauthorized, nil)
}

func TestPublishValidation(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	dir := t.TempDir()

	empty := writeHTML(t, dir, "empty.html", " \n\t ")
	_, errOut, err := runCLI(t, s.home, "", "publish", empty,
		"--server", s.baseURL, "--key", key, "--no-qr")
	if err == nil || !strings.Contains(errOut, "html must not be empty") {
		t.Fatalf("empty HTML error = %v, stderr = %q", err, errOut)
	}

	largePath := filepath.Join(dir, "large.html")
	if err := os.WriteFile(largePath, []byte(strings.Repeat("a", 10*1024*1024+1)), 0o600); err != nil {
		t.Fatalf("write large file: %v", err)
	}
	_, errOut, err = runCLI(t, s.home, "", "publish", largePath,
		"--server", s.baseURL, "--key", key, "--no-qr")
	if err == nil || !strings.Contains(errOut, "page is too large") {
		t.Fatalf("large HTML error = %v, stderr = %q", err, errOut)
	}

	valid := writeHTML(t, dir, "valid.html", "<h1>valid</h1>")
	_, errOut, err = runCLI(t, s.home, "", "publish", valid,
		"--server", s.baseURL, "--key", key, "--ttl=-1s", "--no-qr")
	if err == nil || !strings.Contains(errOut, "ttl must not be negative") {
		t.Fatalf("negative TTL error = %v, stderr = %q", err, errOut)
	}

	_, errOut, err = runCLI(t, s.home, "", "publish", valid,
		"--server", s.baseURL, "--key", key, "--ttl", "721h", "--no-qr")
	if err == nil || !strings.Contains(errOut, "ttl exceeds server maximum") {
		t.Fatalf("large TTL error = %v, stderr = %q", err, errOut)
	}

	_, errOut, err = runCLI(t, s.home, "", "publish", valid,
		"--server", s.baseURL, "--key", key, "--permanent", "--ttl", "1h", "--no-qr")
	if err == nil || !strings.Contains(errOut, "mutually exclusive") {
		t.Fatalf("permanent+TTL error = %v, stderr = %q", err, errOut)
	}

	_, errOut, err = runCLI(t, s.home, "", "publish", filepath.Join(dir, "missing.html"),
		"--server", s.baseURL, "--key", key, "--no-qr")
	if err == nil || errOut == "" {
		t.Fatalf("missing file error = %v, stderr = %q", err, errOut)
	}
}

func TestExpiryAndCleanup(t *testing.T) {
	s := newTestServer(t, func(cfg string) string {
		cfg = setConfigLine(cfg, "grace_period", `"2s"`)
		cfg = setConfigLine(cfg, "cleanup_interval", `"1h"`)
		return cfg
	})
	key := createPublisher(t, s, "publisher")
	path := writeHTML(t, t.TempDir(), "short.html", "<h1>short lived</h1>")

	out, errOut, err := runCLI(t, s.home, "", "publish", path,
		"--server", s.baseURL, "--key", key, "--permanent", "--json")
	if err != nil {
		t.Fatalf("publish: %v\nstderr:\n%s", err, errOut)
	}
	var resp api.PublishResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode publish JSON: %v\n%s", err, out)
	}
	file := filepath.Join(s.dir, "pages", resp.Slug+".html")
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("page file missing before expiry: %v", err)
	}
	if _, errOut, err := runCLI(t, s.home, "", "expire", resp.Slug,
		"--server", s.baseURL, "--key", key); err != nil {
		t.Fatalf("expire: %v\nstderr:\n%s", err, errOut)
	}

	status, _, body := getPage(t, s, resp.Slug, nil)
	assertStatus(t, status, http.StatusGone, body)
	assertContains(t, string(body), "expired")

	// Cleanup compares whole Unix seconds: expires_at < now-grace. Expire sets
	// expires_at to the current second, so wait well past grace before cleanup.
	time.Sleep(4 * time.Second)
	status, _, body = getPage(t, s, resp.Slug, nil)
	assertStatus(t, status, http.StatusNotFound, body)
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("expired page file disappeared before cleanup: %v", err)
	}

	cleanupOut, cleanupErr, err := runCLI(t, s.home, "", "admin", "cleanup",
		"--server", s.baseURL, "--admin-key", s.adminKey, "--json")
	if err != nil {
		t.Fatalf("admin cleanup: %v\nstderr:\n%s", err, cleanupErr)
	}
	var cleanup api.CleanupResponse
	if err := json.Unmarshal([]byte(cleanupOut), &cleanup); err != nil {
		t.Fatalf("decode cleanup JSON: %v\n%s", err, cleanupOut)
	}
	if cleanup.Deleted < 1 {
		t.Fatalf("cleanup deleted = %d, want at least 1", cleanup.Deleted)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("page file still exists after cleanup: %v", err)
	}
	status, _, body = getPage(t, s, resp.Slug, nil)
	assertStatus(t, status, http.StatusNotFound, body)
}

func TestExpireCommand(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	path := writeHTML(t, t.TempDir(), "expire.html", "<h1>expire me</h1>")

	out, errOut, err := runCLI(t, s.home, "", "publish", path,
		"--server", s.baseURL, "--key", key, "--permanent", "--json")
	if err != nil {
		t.Fatalf("publish permanent: %v\nstderr:\n%s", err, errOut)
	}
	var resp api.PublishResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode publish JSON: %v\n%s", err, out)
	}

	_, errOut, err = runCLI(t, s.home, "", "expire", resp.Slug,
		"--server", s.baseURL, "--key", key)
	if err != nil {
		t.Fatalf("expire: %v\nstderr:\n%s", err, errOut)
	}
	assertContains(t, errOut, "expired "+resp.Slug)
	status, _, body := getPage(t, s, resp.Slug, nil)
	assertStatus(t, status, http.StatusGone, body)

	_, errOut, err = runCLI(t, s.home, "", "expire", resp.Slug,
		"--server", s.baseURL, "--key", key)
	if err == nil || !strings.Contains(errOut, "page already expired") {
		t.Fatalf("second expire error = %v, stderr = %q", err, errOut)
	}
}

func TestPublishRateLimit(t *testing.T) {
	s := newTestServer(t, func(cfg string) string {
		return setConfigLine(cfg, "rate_limit_per_hour", "2")
	})
	key := createPublisher(t, s, "publisher")
	req := api.PublishRequest{HTML: "<h1>limited</h1>"}

	for range 2 {
		status, _, body := apiRequest(t, s, http.MethodPost, "/api/pages", key, req)
		assertStatus(t, status, http.StatusCreated, body)
	}
	status, header, body := apiRequest(t, s, http.MethodPost, "/api/pages", key, req)
	assertStatus(t, status, http.StatusTooManyRequests, body)
	assertContains(t, string(body), "rate limit exceeded")
	if header.Get("Retry-After") == "" {
		t.Fatal("429 response has no Retry-After")
	}
}

func TestPublishQRCode(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	path := writeHTML(t, t.TempDir(), "qr.html", "<h1>qr</h1>")

	_, errOut, err := runCLI(t, s.home, "", "publish", path,
		"--server", s.baseURL, "--key", key, "--qr")
	if err != nil {
		t.Fatalf("publish --qr: %v\nstderr:\n%s", err, errOut)
	}
	assertContains(t, errOut, "slug:")
	if !strings.ContainsAny(errOut, "█▀") {
		t.Fatalf("QR code not written to stderr:\n%s", errOut)
	}
}
