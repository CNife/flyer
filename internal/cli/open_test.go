package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOpenPageStatus(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		wantErr  string
		wantOpen bool
	}{
		{"active", http.StatusOK, "", true},
		{"password", http.StatusUnauthorized, "", true},
		{"expired", http.StatusGone, "expired", false},
		{"missing", http.StatusNotFound, "not found", false},
		{"server error", http.StatusInternalServerError, "500", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodHead {
					t.Errorf("method = %s, want HEAD", r.Method)
				}
				if r.URL.Path != "/Ab3dE9xZ" {
					t.Errorf("path = %s, want /Ab3dE9xZ", r.URL.Path)
				}
				w.WriteHeader(c.status)
			}))
			defer srv.Close()
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())

			var opened string
			orig := openBrowser
			openBrowser = func(url string) error {
				opened = url
				return nil
			}
			defer func() { openBrowser = orig }()

			err := cmdOpen([]string{"Ab3dE9xZ", "--server", srv.URL})
			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("cmdOpen: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("cmdOpen error = %v, want containing %q", err, c.wantErr)
			}
			if c.wantOpen {
				if want := srv.URL + "/Ab3dE9xZ"; opened != want {
					t.Fatalf("opened = %q, want %q", opened, want)
				}
			} else if opened != "" {
				t.Fatalf("browser opened %q for an invalid page", opened)
			}
		})
	}
}

func TestOpenRequiresOneSlug(t *testing.T) {
	for _, args := range [][]string{nil, {"one", "two"}} {
		if err := cmdOpen(args); err == nil {
			t.Fatalf("cmdOpen(%v) succeeded, want usage error", args)
		}
	}
}

func TestBrowserCommand(t *testing.T) {
	const url = "https://flyer.cnife.cn/Ab3dE9xZ"
	cases := []struct {
		goos string
		name string
		args []string
	}{
		{"linux", "xdg-open", []string{url}},
		{"darwin", "open", []string{url}},
		{"windows", "rundll32", []string{"url.dll,FileProtocolHandler", url}},
		{"freebsd", "xdg-open", []string{url}},
	}
	for _, c := range cases {
		name, args := browserCommand(c.goos, url)
		if name != c.name {
			t.Errorf("%s: command = %q, want %q", c.goos, name, c.name)
		}
		if len(args) != len(c.args) {
			t.Fatalf("%s: args = %v, want %v", c.goos, args, c.args)
		}
		for i := range args {
			if args[i] != c.args[i] {
				t.Errorf("%s: args = %v, want %v", c.goos, args, c.args)
			}
		}
	}
}
