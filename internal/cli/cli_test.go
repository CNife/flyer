package cli

import (
	"flag"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/CNife/flyer/internal/api"
)

func TestParseTTL(t *testing.T) {
	cases := []struct {
		in    string
		want  int64
		isErr bool
	}{
		{"1h", 3600, false},
		{"24h", 86400, false},
		{"90", 90, false},
		{"0", 0, false},
		{"0s", 0, false},
		{"-1", 0, true},
		{"-1h", 0, true},
		{"nope", 0, true},
	}
	for _, c := range cases {
		got, err := parseTTL(c.in)
		if c.isErr {
			if err == nil {
				t.Errorf("%q: expected error", c.in)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%q: got %d, err %v; want %d", c.in, got, err, c.want)
		}
	}
}

func TestParseFlagsInterspersed(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	ttl := fs.String("ttl", "", "ttl")
	permanent := fs.Bool("permanent", false, "permanent")
	args := []string{"page.html", "--ttl", "24h", "--permanent"}
	if err := parseFlags(fs, args); err != nil {
		t.Fatal(err)
	}
	if *ttl != "24h" || !*permanent {
		t.Fatalf("flags not parsed: ttl=%q permanent=%v", *ttl, *permanent)
	}
	if !reflect.DeepEqual(fs.Args(), []string{"page.html"}) {
		t.Fatalf("positional args = %v", fs.Args())
	}
}

func TestMaskKey(t *testing.T) {
	if got := maskKey(""); got != "(not set)" {
		t.Fatalf("empty mask = %q", got)
	}
	if got := maskKey("fly_1234567890"); got != "fly_1234...7890" {
		t.Fatalf("mask = %q", got)
	}
}

func TestPageState(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	future := now.Add(time.Hour)
	past := now.Add(-time.Hour)
	cases := []struct {
		name    string
		expires *time.Time
		want    string
	}{
		{"permanent", nil, "permanent"},
		{"active", &future, "active"},
		{"expired", &past, "expired"},
		{"boundary", &now, "expired"},
	}
	for _, c := range cases {
		if got := pageState(api.Page{ExpiresAt: c.expires}, now); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestTableCell(t *testing.T) {
	if got := tableCell("  hello\n\tworld  "); got != "hello world" {
		t.Fatalf("tableCell = %q", got)
	}
	long := strings.Repeat("a", 60)
	if got := tableCell(long); len([]rune(got)) != 40 || !strings.HasSuffix(got, "…") {
		t.Fatalf("long tableCell = %q", got)
	}
}

func TestResolveAPIKeyFallsBackToAdminEnv(t *testing.T) {
	t.Setenv("FLYER_KEY", "")
	t.Setenv("FLYER_ADMIN_KEY", "admin-env")
	if got := resolveAPIKey("", ClientConfig{}); got != "admin-env" {
		t.Fatalf("resolveAPIKey = %q, want admin-env", got)
	}
	t.Setenv("FLYER_KEY", "publisher-env")
	if got := resolveAPIKey("", ClientConfig{AdminKey: "config-admin"}); got != "publisher-env" {
		t.Fatalf("FLYER_KEY should win: %q", got)
	}
}

func TestResolveAPIKeyPrefersConfigPublisherOverAdminEnv(t *testing.T) {
	t.Setenv("FLYER_KEY", "")
	t.Setenv("FLYER_ADMIN_KEY", "admin-env")
	if got := resolveAPIKey("", ClientConfig{Key: "publisher-config"}); got != "publisher-config" {
		t.Fatalf("resolveAPIKey = %q, want publisher-config", got)
	}
}
