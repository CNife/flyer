package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// E2E covers active, password, expired, and missing pages; a real server
// cannot produce this internal-error mapping on demand.
func TestOpenServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	err := cmdOpen(newCommand(), []string{"Ab3dE9xZ", "--server", srv.URL})
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("cmdOpen error = %v, want server error 500", err)
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
