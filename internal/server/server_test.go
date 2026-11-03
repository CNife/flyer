package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/CNife/flyer/internal/api"
	"github.com/CNife/flyer/internal/config"
	"github.com/CNife/flyer/internal/store"
)

func newTestServer(t *testing.T, mutate func(*config.Config)) (*Server, config.Config, *httptest.Server) {
	t.Helper()
	cfg := config.Default()
	cfg.BaseURL = "http://example.test"
	cfg.AdminKey = "fly_" + strings.Repeat("a", 40)
	cfg.SessionSecret = strings.Repeat("s", 40)
	cfg.MaxUploadBytes = 1 << 20
	cfg.RateLimitPerHour = 100
	cfg.DefaultTTL = config.Duration{Duration: time.Hour}
	cfg.MaxTTL = config.Duration{Duration: 24 * time.Hour}
	cfg.CleanupInterval = config.Duration{Duration: time.Hour}
	cfg.GracePeriod = config.Duration{Duration: time.Hour}
	if mutate != nil {
		mutate(&cfg)
	}
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv := New(cfg, st, log.New(io.Discard, "", 0))
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, cfg, ts
}

func doJSON(t *testing.T, method, target, token string, body any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, target, reader)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func decode[T any](t *testing.T, resp *http.Response) T {
	t.Helper()
	var v T
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	return v
}

func publish(t *testing.T, ts *httptest.Server, token string, req api.PublishRequest) api.PublishResponse {
	t.Helper()
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/pages", token, req)
	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("publish: status %d: %s", resp.StatusCode, data)
	}
	return decode[api.PublishResponse](t, resp)
}

func TestPublishAndServe(t *testing.T) {
	_, cfg, ts := newTestServer(t, nil)
	page := publish(t, ts, cfg.AdminKey, api.PublishRequest{
		HTML:  "<h1>hello</h1>",
		Title: "Hello",
	})
	if page.Slug == "" || page.URL != "http://example.test/"+page.Slug {
		t.Fatalf("unexpected response: %+v", page)
	}
	if page.Protected || page.ExpiresAt == nil {
		t.Fatalf("unexpected page flags: %+v", page)
	}

	resp, err := http.Get(ts.URL + "/" + page.Slug)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != "<h1>hello</h1>" {
		t.Fatalf("page: status=%d body=%q", resp.StatusCode, body)
	}
	if got := resp.Header.Get("X-Robots-Tag"); !strings.Contains(got, "noindex") {
		t.Fatalf("missing noindex header: %q", got)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("unexpected content type: %q", ct)
	}

	robots, err := http.Get(ts.URL + "/robots.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer robots.Body.Close()
	rb, _ := io.ReadAll(robots.Body)
	if !strings.Contains(string(rb), "Disallow: /") {
		t.Fatalf("unexpected robots.txt: %q", rb)
	}

	missing, err := http.Get(ts.URL + "/doesnotexist")
	if err != nil {
		t.Fatal(err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("missing page status = %d", missing.StatusCode)
	}
}

func TestPublishRequiresKey(t *testing.T) {
	_, _, ts := newTestServer(t, nil)
	for _, token := range []string{"", "fly_wrong"} {
		resp := doJSON(t, http.MethodPost, ts.URL+"/api/pages", token, api.PublishRequest{HTML: "x"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("token %q: status = %d", token, resp.StatusCode)
		}
	}
}

func TestPasswordPage(t *testing.T) {
	_, cfg, ts := newTestServer(t, nil)
	page := publish(t, ts, cfg.AdminKey, api.PublishRequest{
		HTML:     "<h1>secret</h1>",
		Title:    "Protected",
		Password: "hunter2",
	})
	if !page.Protected {
		t.Fatal("page not marked protected")
	}

	prompt, err := http.Get(ts.URL + "/" + page.Slug)
	if err != nil {
		t.Fatal(err)
	}
	promptBody, _ := io.ReadAll(prompt.Body)
	prompt.Body.Close()
	if prompt.StatusCode != http.StatusUnauthorized || !strings.Contains(string(promptBody), "<form") {
		t.Fatalf("prompt: status=%d body=%q", prompt.StatusCode, promptBody)
	}

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	wrong, err := client.PostForm(ts.URL+"/"+page.Slug, url.Values{"password": {"wrong"}})
	if err != nil {
		t.Fatal(err)
	}
	wrongBody, _ := io.ReadAll(wrong.Body)
	wrong.Body.Close()
	if wrong.StatusCode != http.StatusUnauthorized || !strings.Contains(string(wrongBody), "Wrong password") {
		t.Fatalf("wrong password: status=%d body=%q", wrong.StatusCode, wrongBody)
	}

	correct, err := client.PostForm(ts.URL+"/"+page.Slug, url.Values{"password": {"hunter2"}})
	if err != nil {
		t.Fatal(err)
	}
	correct.Body.Close()
	if correct.StatusCode != http.StatusSeeOther || correct.Header.Get("Location") != "/"+page.Slug {
		t.Fatalf("correct password: status=%d location=%q", correct.StatusCode, correct.Header.Get("Location"))
	}
	cookies := correct.Cookies()
	if len(cookies) != 1 || cookies[0].Name != "flyer_session" || !cookies[0].HttpOnly {
		t.Fatalf("unexpected cookies: %+v", cookies)
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/"+page.Slug, nil)
	req.AddCookie(cookies[0])
	unlocked, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	unlockedBody, _ := io.ReadAll(unlocked.Body)
	unlocked.Body.Close()
	if unlocked.StatusCode != http.StatusOK || string(unlockedBody) != "<h1>secret</h1>" {
		t.Fatalf("unlocked: status=%d body=%q", unlocked.StatusCode, unlockedBody)
	}
}

func TestExpiredPage(t *testing.T) {
	srv, cfg, ts := newTestServer(t, nil)
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	srv.now = func() time.Time { return base }
	page := publish(t, ts, cfg.AdminKey, api.PublishRequest{
		HTML:       "expired",
		TTLSeconds: 1,
	})

	srv.now = func() time.Time { return base.Add(2 * time.Second) }
	gone, err := http.Get(ts.URL + "/" + page.Slug)
	if err != nil {
		t.Fatal(err)
	}
	gone.Body.Close()
	if gone.StatusCode != http.StatusGone {
		t.Fatalf("expired status = %d", gone.StatusCode)
	}

	srv.now = func() time.Time { return base.Add(2 * time.Hour) }
	missing, err := http.Get(ts.URL + "/" + page.Slug)
	if err != nil {
		t.Fatal(err)
	}
	missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		t.Fatalf("post-grace status = %d", missing.StatusCode)
	}
}

func TestUploadLimit(t *testing.T) {
	_, cfg, ts := newTestServer(t, func(c *config.Config) {
		c.MaxUploadBytes = 1024
	})
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/pages", cfg.AdminKey, api.PublishRequest{
		HTML: strings.Repeat("a", 2048),
	})
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", resp.StatusCode)
	}
}

func TestRateLimit(t *testing.T) {
	_, cfg, ts := newTestServer(t, func(c *config.Config) {
		c.RateLimitPerHour = 3
	})
	for range 3 {
		publish(t, ts, cfg.AdminKey, api.PublishRequest{HTML: "ok"})
	}
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/pages", cfg.AdminKey, api.PublishRequest{HTML: "ok"})
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Fatal("missing Retry-After header")
	}
}

func TestKeysAndOwnership(t *testing.T) {
	_, cfg, ts := newTestServer(t, nil)
	create := func(name string) api.CreateKeyResponse {
		t.Helper()
		resp := doJSON(t, http.MethodPost, ts.URL+"/api/admin/keys", cfg.AdminKey, api.CreateKeyRequest{Name: name})
		if resp.StatusCode != http.StatusCreated {
			data, _ := io.ReadAll(resp.Body)
			t.Fatalf("create key: status=%d body=%s", resp.StatusCode, data)
		}
		return decode[api.CreateKeyResponse](t, resp)
	}
	k1 := create("one")
	k2 := create("two")
	if k1.Key == "" || k1.Role != "publisher" {
		t.Fatalf("unexpected key: %+v", k1)
	}

	page := publish(t, ts, k1.Key, api.PublishRequest{HTML: "owned"})
	resp := doJSON(t, http.MethodDelete, ts.URL+"/api/pages/"+page.Slug, k2.Key, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other key delete status = %d", resp.StatusCode)
	}
	resp = doJSON(t, http.MethodDelete, ts.URL+"/api/pages/"+page.Slug, k1.Key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner delete status = %d", resp.StatusCode)
	}

	page2 := publish(t, ts, k1.Key, api.PublishRequest{HTML: "second"})
	resp = doJSON(t, http.MethodGet, ts.URL+"/api/admin/stats", k1.Key, nil)
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("publisher stats status = %d", resp.StatusCode)
	}

	resp = doJSON(t, http.MethodDelete, ts.URL+"/api/admin/keys/"+itoa(k1.ID), cfg.AdminKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke status = %d", resp.StatusCode)
	}
	resp = doJSON(t, http.MethodPost, ts.URL+"/api/pages", k1.Key, api.PublishRequest{HTML: "no"})
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked key status = %d", resp.StatusCode)
	}

	resp = doJSON(t, http.MethodGet, ts.URL+"/api/admin/stats", cfg.AdminKey, nil)
	stats := decode[api.StatsResponse](t, resp)
	if stats.Pages != 1 || stats.Keys != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if page2.URL == "" {
		t.Fatal("missing second page")
	}
}

func TestMalformedPublish(t *testing.T) {
	_, cfg, ts := newTestServer(t, nil)
	if resp := doJSON(t, http.MethodPost, ts.URL+"/api/pages", cfg.AdminKey, api.PublishRequest{HTML: ""}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty html status = %d", resp.StatusCode)
	}
	if resp := doJSON(t, http.MethodPost, ts.URL+"/api/pages", cfg.AdminKey, api.PublishRequest{HTML: "x", TTLSeconds: -1}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("negative ttl status = %d", resp.StatusCode)
	}
	if resp := doJSON(t, http.MethodPost, ts.URL+"/api/pages", cfg.AdminKey, api.PublishRequest{HTML: "x", TTLSeconds: int64((48 * time.Hour).Seconds())}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("over max ttl status = %d", resp.StatusCode)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}
