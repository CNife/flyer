package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/CNife/flyer/internal/api"
)

func TestUpdateKeepsURLAndMetadata(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	dir := t.TempDir()
	first := writeHTML(t, dir, "report.html", "<h1>first</h1>")
	pub := publishPage(t, s, key, first, "--title", "Report", "--ttl", "1h")
	before := listPage(t, s, key, pub.Slug)

	second := writeHTML(t, dir, "report2.html", "<h1>second</h1>")
	out, errOut, err := runCLI(t, s.home, "", "update", pub.URL, second,
		"--server", s.baseURL, "--key", key)
	if err != nil {
		t.Fatalf("update: %v\nstderr:\n%s", err, errOut)
	}
	if got := strings.TrimSpace(out); got != pub.URL {
		t.Fatalf("update stdout = %q, want %q", got, pub.URL)
	}
	assertContains(t, errOut, "slug: "+pub.Slug)

	status, _, body := getPage(t, s, pub.Slug, nil)
	assertStatus(t, status, http.StatusOK, body)
	assertContains(t, string(body), "second")
	if strings.Contains(string(body), "first") {
		t.Fatalf("page still serves old content:\n%s", body)
	}

	after := listPage(t, s, key, pub.Slug)
	if after.Title != before.Title {
		t.Fatalf("title = %q, want %q", after.Title, before.Title)
	}
	if !after.CreatedAt.Equal(before.CreatedAt) {
		t.Fatalf("created_at = %v, want %v", after.CreatedAt, before.CreatedAt)
	}
	if !sameTime(after.ExpiresAt, before.ExpiresAt) {
		t.Fatalf("expires_at = %v, want %v", after.ExpiresAt, before.ExpiresAt)
	}

	if _, errOut, err = runCLI(t, s.home, "<h1>stdin update</h1>",
		"update", pub.URL, "-", "--server", s.baseURL, "--key", key); err != nil {
		t.Fatalf("update stdin: %v\nstderr:\n%s", err, errOut)
	}
	status, _, body = getPage(t, s, pub.Slug, nil)
	assertStatus(t, status, http.StatusOK, body)
	assertContains(t, string(body), "stdin update")
}

func TestUpdateTitleTTLAndPermanent(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	dir := t.TempDir()
	first := writeHTML(t, dir, "report.html", "<h1>v1</h1>")
	pub := publishPage(t, s, key, first, "--title", "Old", "--ttl", "1h")
	second := writeHTML(t, dir, "report2.html", "<h1>v2</h1>")

	out, errOut, err := runCLI(t, s.home, "", "update", pub.Slug, second,
		"--server", s.baseURL, "--key", key, "--title", "New", "--ttl", "3h", "--json")
	if err != nil {
		t.Fatalf("update ttl: %v\nstderr:\n%s", err, errOut)
	}
	var updated api.PublishResponse
	if err := json.Unmarshal([]byte(out), &updated); err != nil {
		t.Fatalf("decode update JSON: %v\n%s", err, out)
	}
	if updated.ExpiresAt == nil || !updated.ExpiresAt.After(*pub.ExpiresAt) {
		t.Fatalf("expires_at = %v, want after %v", updated.ExpiresAt, pub.ExpiresAt)
	}
	page := listPage(t, s, key, pub.Slug)
	if page.Title != "New" {
		t.Fatalf("title = %q, want New", page.Title)
	}
	if page.ExpiresAt == nil || !page.ExpiresAt.After(*pub.ExpiresAt) {
		t.Fatalf("listed expires_at = %v, want after %v", page.ExpiresAt, pub.ExpiresAt)
	}

	out, errOut, err = runCLI(t, s.home, "", "update", pub.Slug, second,
		"--server", s.baseURL, "--key", key, "--permanent", "--json")
	if err != nil {
		t.Fatalf("update permanent: %v\nstderr:\n%s", err, errOut)
	}
	updated = api.PublishResponse{}
	if err := json.Unmarshal([]byte(out), &updated); err != nil {
		t.Fatalf("decode permanent JSON: %v\n%s", err, out)
	}
	if updated.ExpiresAt != nil {
		t.Fatalf("expires_at = %v, want nil", updated.ExpiresAt)
	}
	page = listPage(t, s, key, pub.Slug)
	if page.ExpiresAt != nil {
		t.Fatalf("listed expires_at = %v, want nil", page.ExpiresAt)
	}

	out, errOut, err = runCLI(t, s.home, "", "update", pub.Slug, second,
		"--server", s.baseURL, "--key", key, "--json")
	if err != nil {
		t.Fatalf("update keep permanent: %v\nstderr:\n%s", err, errOut)
	}
	updated = api.PublishResponse{}
	if err := json.Unmarshal([]byte(out), &updated); err != nil {
		t.Fatalf("decode keep permanent JSON: %v\n%s", err, out)
	}
	if updated.ExpiresAt != nil {
		t.Fatalf("expires_at = %v, want nil", updated.ExpiresAt)
	}

	_, errOut, err = runCLI(t, s.home, "", "update", pub.Slug, second,
		"--server", s.baseURL, "--key", key, "--permanent", "--ttl", "1h")
	if err == nil || !strings.Contains(errOut, "mutually exclusive") {
		t.Fatalf("permanent+ttl error = %v, stderr = %q", err, errOut)
	}
}

func TestUpdateOwnershipAndExpiry(t *testing.T) {
	s := newTestServer(t, nil)
	owner := createPublisher(t, s, "owner")
	other := createPublisher(t, s, "other")
	dir := t.TempDir()
	path := writeHTML(t, dir, "report.html", "<h1>owned</h1>")
	pub := publishPage(t, s, owner, path, "--ttl", "1h")
	update := writeHTML(t, dir, "update.html", "<h1>changed</h1>")

	_, errOut, err := runCLI(t, s.home, "", "update", pub.Slug, update,
		"--server", s.baseURL, "--key", other)
	if err == nil || !strings.Contains(errOut, "page not found") {
		t.Fatalf("foreign update: err=%v stderr=%q", err, errOut)
	}

	if _, errOut, err = runCLI(t, s.home, "", "update", pub.Slug, update,
		"--server", s.baseURL, "--key", s.adminKey); err != nil {
		t.Fatalf("admin update: %v\nstderr:\n%s", err, errOut)
	}
	status, _, body := getPage(t, s, pub.Slug, nil)
	assertStatus(t, status, http.StatusOK, body)
	assertContains(t, string(body), "changed")

	if _, errOut, err = runCLI(t, s.home, "", "expire", pub.Slug,
		"--server", s.baseURL, "--key", owner); err != nil {
		t.Fatalf("expire: %v\nstderr:\n%s", err, errOut)
	}
	_, errOut, err = runCLI(t, s.home, "", "update", pub.Slug, update,
		"--server", s.baseURL, "--key", owner)
	if err == nil || !strings.Contains(errOut, "page already expired") {
		t.Fatalf("expired update: err=%v stderr=%q", err, errOut)
	}

	_, errOut, err = runCLI(t, s.home, "", "update", "ZZZZ9999", update,
		"--server", s.baseURL, "--key", owner)
	if err == nil || !strings.Contains(errOut, "page not found") {
		t.Fatalf("missing update: err=%v stderr=%q", err, errOut)
	}
}

func TestUpdateValidation(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	dir := t.TempDir()
	valid := writeHTML(t, dir, "valid.html", "<h1>valid</h1>")
	pub := publishPage(t, s, key, valid)
	empty := writeHTML(t, dir, "empty.html", " \n\t ")

	if _, errOut, err := runCLI(t, s.home, "", "update", pub.Slug, empty,
		"--server", s.baseURL, "--key", key); err == nil || !strings.Contains(errOut, "html must not be empty") {
		t.Fatalf("empty HTML error = %v, stderr = %q", err, errOut)
	}

	large := filepath.Join(dir, "large.html")
	if err := os.WriteFile(large, []byte(strings.Repeat("a", 10*1024*1024+1)), 0o600); err != nil {
		t.Fatalf("write large file: %v", err)
	}
	if _, errOut, err := runCLI(t, s.home, "", "update", pub.Slug, large,
		"--server", s.baseURL, "--key", key); err == nil || !strings.Contains(errOut, "page is too large") {
		t.Fatalf("large HTML error = %v, stderr = %q", err, errOut)
	}

	if _, errOut, err := runCLI(t, s.home, "", "update", pub.Slug, valid,
		"--server", s.baseURL, "--key", key, "--ttl=-1s"); err == nil || !strings.Contains(errOut, "ttl must not be negative") {
		t.Fatalf("negative TTL error = %v, stderr = %q", err, errOut)
	}
	if _, errOut, err := runCLI(t, s.home, "", "update", pub.Slug, valid,
		"--server", s.baseURL, "--key", key, "--ttl", "721h"); err == nil || !strings.Contains(errOut, "ttl exceeds server maximum") {
		t.Fatalf("large TTL error = %v, stderr = %q", err, errOut)
	}
	if _, errOut, err := runCLI(t, s.home, "", "update", pub.Slug, valid,
		"--server", s.baseURL, "--key", key, "--ttl", "0"); err == nil || !strings.Contains(errOut, "must be positive") {
		t.Fatalf("zero TTL error = %v, stderr = %q", err, errOut)
	}

	status, _, body := apiRequest(t, s, http.MethodPut, "/api/pages/"+pub.Slug, key, api.UpdateRequest{
		HTML:       "<h1>x</h1>",
		Permanent:  true,
		TTLSeconds: 3600,
	})
	assertStatus(t, status, http.StatusBadRequest, body)
	assertContains(t, string(body), "mutually exclusive")
}

func TestUpdatePasswordAndConditionalGet(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "publisher")
	dir := t.TempDir()
	first := writeHTML(t, dir, "secret.html", "<h1>first secret</h1>")
	pub := publishPage(t, s, key, first, "--password", "correct horse")
	if !pub.Protected {
		t.Fatal("password page not marked protected")
	}
	before := listPage(t, s, key, pub.Slug)
	second := writeHTML(t, dir, "secret2.html", "<h1>second secret</h1>")

	out, errOut, err := runCLI(t, s.home, "", "update", pub.Slug, second,
		"--server", s.baseURL, "--key", key, "--json")
	if err != nil {
		t.Fatalf("update password page: %v\nstderr:\n%s", err, errOut)
	}
	var updated api.PublishResponse
	if err := json.Unmarshal([]byte(out), &updated); err != nil {
		t.Fatalf("decode update JSON: %v\n%s", err, out)
	}
	if !updated.Protected {
		t.Fatal("updated page is no longer protected")
	}

	status, _, body := getPage(t, s, pub.Slug, nil)
	assertStatus(t, status, http.StatusUnauthorized, body)
	assertContains(t, string(body), `type="password"`)

	jar := newCookieJar(t)
	status, body = postPassword(t, s, pub.Slug, "correct horse", jar)
	assertStatus(t, status, http.StatusOK, body)
	assertContains(t, string(body), "second secret")

	raw := s.baseURL + "/" + pub.Slug
	assertLoopback(t, raw)
	req, err := http.NewRequest(http.MethodGet, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("If-Modified-Since", before.CreatedAt.UTC().Format(http.TimeFormat))
	client := &http.Client{Timeout: 10 * time.Second, Jar: jar}
	condResp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(condResp.Body)
	_ = condResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	assertStatus(t, condResp.StatusCode, http.StatusOK, data)
	assertContains(t, string(data), "second secret")
	if lm := condResp.Header.Get("Last-Modified"); lm != "" {
		t.Fatalf("Last-Modified = %q, want empty", lm)
	}
}

func publishPage(t *testing.T, s *testServer, key, path string, extra ...string) api.PublishResponse {
	t.Helper()
	args := []string{"publish", path, "--server", s.baseURL, "--key", key, "--json"}
	args = append(args, extra...)
	out, errOut, err := runCLI(t, s.home, "", args...)
	if err != nil {
		t.Fatalf("publish: %v\nstderr:\n%s", err, errOut)
	}
	var resp api.PublishResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode publish JSON: %v\n%s", err, out)
	}
	return resp
}

func listPage(t *testing.T, s *testServer, key, slug string) api.Page {
	t.Helper()
	out, errOut, err := runCLI(t, s.home, "", "list", "--server", s.baseURL, "--key", key, "--json")
	if err != nil {
		t.Fatalf("list: %v\nstderr:\n%s", err, errOut)
	}
	var resp api.PageListResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode list JSON: %v\n%s", err, out)
	}
	for _, p := range resp.Pages {
		if p.Slug == slug {
			return p
		}
	}
	t.Fatalf("page %s not found in list", slug)
	return api.Page{}
}

func sameTime(a, b *time.Time) bool {
	if (a == nil) != (b == nil) {
		return false
	}
	return a == nil || a.Equal(*b)
}
