package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/CNife/flyer/internal/api"
)

var (
	flyerBinary string
	repoRoot    string
)

func TestMain(m *testing.M) {
	wd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: getwd: %v\n", err)
		os.Exit(1)
	}
	repoRoot = filepath.Clean(filepath.Join(wd, "..", ".."))

	dir, err := os.MkdirTemp("", "flyer-e2e-bin-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: temp dir: %v\n", err)
		os.Exit(1)
	}
	name := "flyer"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	flyerBinary = filepath.Join(dir, name)
	build := exec.Command("go", "build", "-trimpath", "-o", flyerBinary, "./cmd/flyer")
	build.Dir = repoRoot
	build.Env = replaceEnv(os.Environ(), "CGO_ENABLED", "0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "e2e: build flyer: %v\n%s", err, out)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

type testServer struct {
	dir      string
	home     string
	baseURL  string
	adminKey string
	port     int

	cmd     *exec.Cmd
	stderr  *bytes.Buffer
	exited  chan struct{}
	waitErr error
}

// initServer creates a temporary server directory and config, and returns the
// metadata needed to start it. editConfig, when non-nil, can rewrite the
// generated TOML before the server starts (for short grace periods, etc.).
func initServer(t *testing.T, editConfig func(string) string) (dir, home, baseURL, adminKey string, port int) {
	t.Helper()
	dir = t.TempDir()
	home = t.TempDir()
	port = freePort(t)
	baseURL = fmt.Sprintf("http://127.0.0.1:%d", port)
	listen := fmt.Sprintf("127.0.0.1:%d", port)

	out, errOut, err := runCLI(t, home, "", "init-server",
		"--dir", dir, "--base-url", baseURL, "--listen", listen)
	if err != nil {
		t.Fatalf("init-server: %v\nstderr:\n%s", err, errOut)
	}
	adminKey = strings.TrimSpace(out)
	if !strings.HasPrefix(adminKey, "fly_") {
		t.Fatalf("init-server stdout = %q, want an admin key", out)
	}
	if editConfig != nil {
		path := filepath.Join(dir, "config.toml")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read generated config: %v", err)
		}
		if err := os.WriteFile(path, []byte(editConfig(string(data))), 0o600); err != nil {
			t.Fatalf("rewrite generated config: %v", err)
		}
	}
	return dir, home, baseURL, adminKey, port
}

func newTestServer(t *testing.T, editConfig func(string) string) *testServer {
	t.Helper()
	dir, home, baseURL, adminKey, port := initServer(t, editConfig)
	s := &testServer{
		dir:      dir,
		home:     home,
		baseURL:  baseURL,
		adminKey: adminKey,
		port:     port,
	}
	s.start(t)
	t.Cleanup(func() { s.stop(t) })
	return s
}

func (s *testServer) start(t *testing.T) {
	t.Helper()
	s.stderr = &bytes.Buffer{}
	s.exited = make(chan struct{})
	s.waitErr = nil

	cmd := exec.Command(flyerBinary, "serve", "--dir", s.dir)
	cmd.Dir = repoRoot
	cmd.Env = cleanEnv(s.home, nil)
	cmd.Stdout = io.Discard
	cmd.Stderr = s.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	s.cmd = cmd
	go func() {
		s.waitErr = cmd.Wait()
		close(s.exited)
	}()

	if err := s.waitHealthy(10 * time.Second); err != nil {
		s.stop(t)
		t.Fatalf("server did not become healthy: %v\nstderr:\n%s", err, s.stderr.String())
	}
}

func (s *testServer) waitHealthy(timeout time.Duration) error {
	client := &http.Client{Timeout: 300 * time.Millisecond}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		select {
		case <-s.exited:
			if s.waitErr != nil {
				return fmt.Errorf("server exited during startup: %w", s.waitErr)
			}
			return errors.New("server exited during startup")
		default:
		}
		resp, err := client.Get(s.baseURL + "/health")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
			lastErr = fmt.Errorf("health status %d", resp.StatusCode)
		} else {
			lastErr = err
		}
		time.Sleep(50 * time.Millisecond)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("timed out")
	}
	return lastErr
}

func (s *testServer) stop(t *testing.T) {
	t.Helper()
	if s.cmd == nil || s.cmd.Process == nil {
		return
	}
	select {
	case <-s.exited:
		return
	default:
	}
	if runtime.GOOS == "windows" {
		_ = s.cmd.Process.Kill()
	} else {
		_ = s.cmd.Process.Signal(os.Interrupt)
	}
	select {
	case <-s.exited:
	case <-time.After(5 * time.Second):
		_ = s.cmd.Process.Kill()
		<-s.exited
		t.Logf("server did not stop after interrupt; killed")
	}
}

func (s *testServer) logs() string {
	if s.stderr == nil {
		return ""
	}
	return s.stderr.String()
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	defer func() { _ = l.Close() }()
	return l.Addr().(*net.TCPAddr).Port
}

func runCLI(t *testing.T, home, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runCLIEnv(t, home, nil, stdin, args...)
}

func runCLIEnv(t *testing.T, home string, extraEnv []string, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	return runCLIRaw(home, extraEnv, stdin, args...)
}

func runCLIRaw(home string, extraEnv []string, stdin string, args ...string) (stdout, stderr string, err error) {
	cmd := exec.Command(flyerBinary, args...)
	cmd.Dir = repoRoot
	cmd.Env = cleanEnv(home, extraEnv)
	cmd.Stdin = strings.NewReader(stdin)
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err = cmd.Run()
	return out.String(), errOut.String(), err
}

func cleanEnv(home string, extra []string) []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		key, _, _ := strings.Cut(kv, "=")
		switch {
		case strings.HasPrefix(key, "FLYER_"),
			key == "HOME",
			key == "XDG_CONFIG_HOME",
			key == "APPDATA",
			key == "LOCALAPPDATA":
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+home,
		"XDG_CONFIG_HOME="+home,
		"APPDATA="+home,
		"LOCALAPPDATA="+home,
	)
	for _, kv := range extra {
		key, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		env = replaceEnv(env, key, value)
	}
	return env
}

func replaceEnv(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func apiRequest(t *testing.T, s *testServer, method, path, token string, payload any) (status int, header http.Header, body []byte) {
	t.Helper()
	var reader io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode request: %v", err)
		}
		reader = bytes.NewReader(data)
	}
	rawURL := s.baseURL + path
	assertLoopback(t, rawURL)
	req, err := http.NewRequest(method, rawURL, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read %s %s: %v", method, path, err)
	}
	return resp.StatusCode, resp.Header, data
}

func publishAPI(t *testing.T, s *testServer, key string, req api.PublishRequest) *api.PublishResponse {
	t.Helper()
	status, _, body := apiRequest(t, s, http.MethodPost, "/api/pages", key, req)
	if status != http.StatusCreated {
		t.Fatalf("publish status = %d, body = %s", status, body)
	}
	var resp api.PublishResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode publish response: %v\n%s", err, body)
	}
	if resp.Slug == "" || resp.URL == "" {
		t.Fatalf("publish response = %+v", resp)
	}
	return &resp
}

func createPublisher(t *testing.T, s *testServer, name string) string {
	t.Helper()
	out, errOut, err := runCLI(t, s.home, "", "admin", "key", "create",
		"--server", s.baseURL, "--admin-key", s.adminKey, "--name", name, "--json")
	if err != nil {
		t.Fatalf("create publisher: %v\nstderr:\n%s", err, errOut)
	}
	var resp api.CreateKeyResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("decode create key response: %v\n%s", err, out)
	}
	if resp.ID <= 0 || !strings.HasPrefix(resp.Key, "fly_") {
		t.Fatalf("create key response = %+v", resp)
	}
	return resp.Key
}

func getPage(t *testing.T, s *testServer, slug string, jar http.CookieJar) (status int, header http.Header, body []byte) {
	t.Helper()
	rawURL := s.baseURL + "/" + url.PathEscape(slug)
	assertLoopback(t, rawURL)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		t.Fatalf("build page request: %v", err)
	}
	client := &http.Client{Timeout: 10 * time.Second, Jar: jar}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("get page %s: %v", slug, err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read page %s: %v", slug, err)
	}
	return resp.StatusCode, resp.Header, data
}

func postPassword(t *testing.T, s *testServer, slug, password string, jar http.CookieJar) (status int, body []byte) {
	t.Helper()
	rawURL := s.baseURL + "/" + url.PathEscape(slug)
	assertLoopback(t, rawURL)
	form := url.Values{"password": {password}}
	req, err := http.NewRequest(http.MethodPost, rawURL, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build password request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := &http.Client{Timeout: 10 * time.Second, Jar: jar}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("post password: %v", err)
	}
	data, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read password response: %v", err)
	}
	return resp.StatusCode, data
}

func newCookieJar(t *testing.T) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatalf("new cookie jar: %v", err)
	}
	return jar
}

func slugFromURL(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	slug := strings.TrimPrefix(u.Path, "/")
	if slug == "" || strings.Contains(slug, "/") {
		t.Fatalf("URL %q does not contain a single slug", raw)
	}
	return slug
}

func assertLoopback(t *testing.T, raw string) {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse URL %q: %v", raw, err)
	}
	switch u.Hostname() {
	case "127.0.0.1", "::1", "localhost":
	default:
		t.Fatalf("refusing non-loopback URL %q", raw)
	}
}

func assertStatus(t *testing.T, got, want int, body []byte) {
	t.Helper()
	if got != want {
		t.Fatalf("status = %d, want %d; body:\n%s", got, want, body)
	}
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("output does not contain %q:\n%s", want, got)
	}
}

func setConfigLine(cfg, key, value string) string {
	prefix := key + " = "
	lines := strings.Split(cfg, "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[i] = prefix + value
			return strings.Join(lines, "\n")
		}
	}
	return cfg + "\n" + prefix + value + "\n"
}

func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if err == nil && len(data) > 0 {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("file %s was not written", path)
	return ""
}

func writeHTML(t *testing.T, dir, name, html string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(html), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}
