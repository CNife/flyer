package e2e

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/CNife/flyer/internal/api"
)

func TestInitServerWritesPrivateConfig(t *testing.T) {
	dir, _, baseURL, adminKey, port := initServer(t, nil)
	if !strings.HasPrefix(adminKey, "fly_") || len(adminKey) < 20 {
		t.Fatalf("admin key = %q", adminKey)
	}

	cfgPath := filepath.Join(dir, "config.toml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	cfg := string(data)
	for _, want := range []string{
		`base_url = "` + baseURL + `"`,
		`listen = "127.0.0.1:` + itoa(port) + `"`,
		`admin_key = "` + adminKey + `"`,
	} {
		assertContains(t, cfg, want)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(cfgPath)
		if err != nil {
			t.Fatalf("stat config: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Fatalf("config mode = %o, want 600", mode)
		}
		info, err = os.Stat(filepath.Join(dir, "pages"))
		if err != nil {
			t.Fatalf("stat pages dir: %v", err)
		}
		if mode := info.Mode().Perm(); mode != 0o750 {
			t.Fatalf("pages mode = %o, want 750", mode)
		}
	}

	_, errOut, err := runCLI(t, t.TempDir(), "", "init-server",
		"--dir", dir, "--base-url", baseURL, "--listen", "127.0.0.1:"+itoa(port))
	if err == nil {
		t.Fatal("second init-server succeeded, want overwrite refusal")
	}
	assertContains(t, errOut, "already exists")

	after, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatalf("read config after second init: %v", err)
	}
	if string(after) != cfg {
		t.Fatal("second init-server changed the existing config")
	}
}

func TestServeHealthRobotsAndSecurityHeaders(t *testing.T) {
	s := newTestServer(t, nil)

	status, header, body := getRaw(t, s.baseURL+"/health")
	assertStatus(t, status, http.StatusOK, body)
	if string(body) != "ok\n" {
		t.Fatalf("health body = %q", body)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q", got)
	}
	if got := header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Fatalf("Referrer-Policy = %q", got)
	}

	status, _, body = getRaw(t, s.baseURL+"/robots.txt")
	assertStatus(t, status, http.StatusOK, body)
	assertContains(t, string(body), "Disallow: /")

	status, _, body = getRaw(t, s.baseURL+"/bad!")
	assertStatus(t, status, http.StatusNotFound, body)
}

func TestPagePersistsAcrossRestart(t *testing.T) {
	s := newTestServer(t, nil)
	key := createPublisher(t, s, "restart")
	path := writeHTML(t, t.TempDir(), "restart.html", "<h1>persisted</h1>")

	out, errOut, err := runCLI(t, s.home, "", "publish", path,
		"--server", s.baseURL, "--key", key, "--no-qr")
	if err != nil {
		t.Fatalf("publish: %v\nstderr:\n%s", err, errOut)
	}
	slug := slugFromURL(t, strings.TrimSpace(out))

	status, _, body := getPage(t, s, slug, nil)
	assertStatus(t, status, http.StatusOK, body)
	assertContains(t, string(body), "persisted")

	s.stop(t)
	s.start(t)

	status, _, body = getPage(t, s, slug, nil)
	assertStatus(t, status, http.StatusOK, body)
	assertContains(t, string(body), "persisted")

	status, _, body = apiRequest(t, s, http.MethodGet, "/api/pages", key, nil)
	assertStatus(t, status, http.StatusOK, body)
	var list api.PageListResponse
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("decode list: %v\n%s", err, body)
	}
	if list.Total != 1 || len(list.Pages) != 1 || list.Pages[0].Slug != slug {
		t.Fatalf("list after restart = %+v", list)
	}
}

func TestGracefulShutdown(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no SIGINT graceful-shutdown path")
	}
	s := newTestServer(t, nil)
	s.stop(t)
	if s.waitErr != nil {
		t.Fatalf("server exit after interrupt = %v\nstderr:\n%s", s.waitErr, s.logs())
	}
}

func TestServeWithoutConfigFails(t *testing.T) {
	_, errOut, err := runCLI(t, t.TempDir(), "", "serve", "--dir", t.TempDir())
	if err == nil {
		t.Fatal("serve without config succeeded")
	}
	assertContains(t, errOut, "config.toml")
}

func getRaw(t *testing.T, rawURL string) (status int, header http.Header, body []byte) {
	t.Helper()
	assertLoopback(t, rawURL)
	resp, err := http.Get(rawURL)
	if err != nil {
		t.Fatalf("GET %s: %v", rawURL, err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read %s: %v", rawURL, err)
	}
	return resp.StatusCode, resp.Header, data
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
