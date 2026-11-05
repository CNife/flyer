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

func publishPage(t *testing.T, ts *httptest.Server, token string, req api.PublishRequest) api.PublishResponse {
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
	page := publishPage(t, ts, cfg.AdminKey, api.PublishRequest{
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
	page := publishPage(t, ts, cfg.AdminKey, api.PublishRequest{
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
	page := publishPage(t, ts, cfg.AdminKey, api.PublishRequest{
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
	t.Run("module limit maps to 413", func(t *testing.T) {
		resp := doJSON(t, http.MethodPost, ts.URL+"/api/pages", cfg.AdminKey, api.PublishRequest{
			HTML: strings.Repeat("a", 2048),
		})
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})
	t.Run("body guard rejects oversized payloads", func(t *testing.T) {
		resp := doJSON(t, http.MethodPost, ts.URL+"/api/pages", cfg.AdminKey, api.PublishRequest{
			HTML: strings.Repeat("a", 1<<20),
		})
		if resp.StatusCode != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d", resp.StatusCode)
		}
	})
}

func TestRateLimit(t *testing.T) {
	_, cfg, ts := newTestServer(t, func(c *config.Config) {
		c.RateLimitPerHour = 3
	})
	for range 3 {
		publishPage(t, ts, cfg.AdminKey, api.PublishRequest{HTML: "ok"})
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

	page := publishPage(t, ts, k1.Key, api.PublishRequest{HTML: "owned"})
	resp := doJSON(t, http.MethodDelete, ts.URL+"/api/pages/"+page.Slug, k2.Key, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other key delete status = %d", resp.StatusCode)
	}
	resp = doJSON(t, http.MethodDelete, ts.URL+"/api/pages/"+page.Slug, k1.Key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner delete status = %d", resp.StatusCode)
	}

	page2 := publishPage(t, ts, k1.Key, api.PublishRequest{HTML: "second"})
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

func TestPermanentPage(t *testing.T) {
	srv, cfg, ts := newTestServer(t, nil)
	page := publishPage(t, ts, cfg.AdminKey, api.PublishRequest{
		HTML:      "forever",
		Permanent: true,
	})
	if page.ExpiresAt != nil {
		t.Fatalf("permanent page has expiry: %v", page.ExpiresAt)
	}
	srv.now = func() time.Time { return time.Now().Add(10 * 365 * 24 * time.Hour) }
	resp, err := http.Get(ts.URL + "/" + page.Slug)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("permanent page status = %d", resp.StatusCode)
	}
}

func TestMalformedPublish(t *testing.T) {
	_, cfg, ts := newTestServer(t, nil)
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/pages", cfg.AdminKey, api.PublishRequest{HTML: ""})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty html status = %d", resp.StatusCode)
	}
	if got := decode[api.ErrorResponse](t, resp).Error; got != "html must not be empty" {
		t.Fatalf("error = %q", got)
	}

	resp = doJSON(t, http.MethodPost, ts.URL+"/api/pages", cfg.AdminKey, api.PublishRequest{HTML: "x", TTLSeconds: -1})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("negative ttl status = %d", resp.StatusCode)
	}
	resp = doJSON(t, http.MethodPost, ts.URL+"/api/pages", cfg.AdminKey, api.PublishRequest{
		HTML:       "x",
		TTLSeconds: int64(cfg.MaxTTL.Duration/time.Second) + 1,
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("over max ttl status = %d", resp.StatusCode)
	}
}

func itoa(v int64) string {
	return strconv.FormatInt(v, 10)
}

func createPublisherKey(t *testing.T, ts *httptest.Server, adminKey, name string) api.CreateKeyResponse {
	t.Helper()
	resp := doJSON(t, http.MethodPost, ts.URL+"/api/admin/keys", adminKey, api.CreateKeyRequest{Name: name})
	if resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("create key %s: status=%d body=%s", name, resp.StatusCode, data)
	}
	return decode[api.CreateKeyResponse](t, resp)
}

func TestListPages(t *testing.T) {
	srv, cfg, ts := newTestServer(t, nil)
	k1 := createPublisherKey(t, ts, cfg.AdminKey, "one")
	k2 := createPublisherKey(t, ts, cfg.AdminKey, "two")
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	srv.now = func() time.Time { return base }
	first := publishPage(t, ts, k1.Key, api.PublishRequest{HTML: "first", Title: "First"})

	srv.now = func() time.Time { return base.Add(time.Second) }
	second := publishPage(t, ts, k1.Key, api.PublishRequest{HTML: "second", Title: "Second", Permanent: true})

	srv.now = func() time.Time { return base.Add(2 * time.Second) }
	publishPage(t, ts, k2.Key, api.PublishRequest{HTML: "other"})

	srv.now = func() time.Time { return base.Add(3 * time.Second) }
	adminPage := publishPage(t, ts, cfg.AdminKey, api.PublishRequest{HTML: "admin"})

	resp := doJSON(t, http.MethodGet, ts.URL+"/api/pages", k1.Key, nil)
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("list status=%d body=%s", resp.StatusCode, data)
	}
	list := decode[api.PageListResponse](t, resp)
	if list.Total != 2 || len(list.Pages) != 2 || list.Limit != 100 || list.Offset != 0 {
		t.Fatalf("unexpected list: %+v", list)
	}
	if list.Pages[0].Slug != second.Slug || list.Pages[1].Slug != first.Slug {
		t.Fatalf("unexpected order: %+v", list.Pages)
	}
	if list.Pages[0].ExpiresAt != nil || list.Pages[1].ExpiresAt == nil {
		t.Fatalf("unexpected expiries: %+v", list.Pages)
	}
	if list.Pages[0].URL != "http://example.test/"+second.Slug {
		t.Fatalf("unexpected URL: %q", list.Pages[0].URL)
	}
	if list.Pages[0].OwnerKeyID == nil || *list.Pages[0].OwnerKeyID != k1.ID {
		t.Fatalf("unexpected owner: %+v", list.Pages[0])
	}

	resp = doJSON(t, http.MethodGet, ts.URL+"/api/pages?limit=1&offset=1", k1.Key, nil)
	paged := decode[api.PageListResponse](t, resp)
	if paged.Total != 2 || len(paged.Pages) != 1 || paged.Pages[0].Slug != first.Slug {
		t.Fatalf("unexpected paged list: %+v", paged)
	}

	resp = doJSON(t, http.MethodGet, ts.URL+"/api/pages", cfg.AdminKey, nil)
	all := decode[api.PageListResponse](t, resp)
	if all.Total != 4 || len(all.Pages) != 4 || all.Pages[0].Slug != adminPage.Slug {
		t.Fatalf("unexpected admin list: %+v", all)
	}
	if all.Pages[0].OwnerKeyID != nil {
		t.Fatalf("admin page has owner: %+v", all.Pages[0])
	}

	for _, query := range []string{"limit=0", "limit=501", "limit=x", "offset=-1", "offset=x"} {
		resp = doJSON(t, http.MethodGet, ts.URL+"/api/pages?"+query, k1.Key, nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s: status=%d", query, resp.StatusCode)
		}
	}

	resp = doJSON(t, http.MethodGet, ts.URL+"/api/pages", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing key status=%d", resp.StatusCode)
	}
}

func TestExpirePage(t *testing.T) {
	srv, cfg, ts := newTestServer(t, nil)
	k1 := createPublisherKey(t, ts, cfg.AdminKey, "one")
	k2 := createPublisherKey(t, ts, cfg.AdminKey, "two")
	base := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	srv.now = func() time.Time { return base }
	page := publishPage(t, ts, k1.Key, api.PublishRequest{HTML: "owned"})

	resp := doJSON(t, http.MethodPost, ts.URL+"/api/pages/"+page.Slug+"/expire", k2.Key, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("other key expire status=%d", resp.StatusCode)
	}
	resp = doJSON(t, http.MethodPost, ts.URL+"/api/pages/"+page.Slug+"/expire", "", nil)
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("missing key expire status=%d", resp.StatusCode)
	}

	resp = doJSON(t, http.MethodPost, ts.URL+"/api/pages/"+page.Slug+"/expire", k1.Key, nil)
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		t.Fatalf("owner expire status=%d body=%s", resp.StatusCode, data)
	}
	expired := decode[api.ExpirePageResponse](t, resp)
	if !expired.Expired || !expired.ExpiresAt.Equal(base) {
		t.Fatalf("unexpected expire response: %+v", expired)
	}

	gone, err := http.Get(ts.URL + "/" + page.Slug)
	if err != nil {
		t.Fatal(err)
	}
	gone.Body.Close()
	if gone.StatusCode != http.StatusGone {
		t.Fatalf("expired page status=%d", gone.StatusCode)
	}

	resp = doJSON(t, http.MethodPost, ts.URL+"/api/pages/"+page.Slug+"/expire", k1.Key, nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second expire status=%d", resp.StatusCode)
	}

	permanent := publishPage(t, ts, k1.Key, api.PublishRequest{HTML: "forever", Permanent: true})
	resp = doJSON(t, http.MethodPost, ts.URL+"/api/pages/"+permanent.Slug+"/expire", k1.Key, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("permanent expire status=%d", resp.StatusCode)
	}
	gone, err = http.Get(ts.URL + "/" + permanent.Slug)
	if err != nil {
		t.Fatal(err)
	}
	gone.Body.Close()
	if gone.StatusCode != http.StatusGone {
		t.Fatalf("expired permanent page status=%d", gone.StatusCode)
	}

	adminExpire := publishPage(t, ts, k1.Key, api.PublishRequest{HTML: "admin can expire"})
	resp = doJSON(t, http.MethodPost, ts.URL+"/api/pages/"+adminExpire.Slug+"/expire", cfg.AdminKey, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("admin expire status=%d", resp.StatusCode)
	}

	resp = doJSON(t, http.MethodPost, ts.URL+"/api/pages/notthere/expire", k1.Key, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("missing page expire status=%d", resp.StatusCode)
	}
	resp = doJSON(t, http.MethodPost, ts.URL+"/api/pages/zz/expire", k1.Key, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("invalid slug expire status=%d", resp.StatusCode)
	}
}
