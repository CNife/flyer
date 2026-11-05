package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
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
		{"-500ms", 0, true},
		{"500ms", 0, true},
		{"1500ms", 0, true},
		{"nope", 0, true},
		{"2s", 2, false},
		{"1m30s", 90, false},
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

// fakeAPI is the in-process adapter behind the command's client seam.
type fakeAPI struct {
	published *api.PublishRequest
	publish   *api.PublishResponse
	pages     *api.PageListResponse
	status    int
	expire    *api.ExpirePageResponse
	deleted   []string
	keys      []api.Key
	created   *api.CreateKeyResponse
	stats     *api.StatsResponse
	cleanup   *api.CleanupResponse
}

func (f *fakeAPI) Publish(_ context.Context, req api.PublishRequest) (*api.PublishResponse, error) {
	f.published = &req
	return f.publish, nil
}

func (f *fakeAPI) ListPages(context.Context, int, int) (*api.PageListResponse, error) {
	return f.pages, nil
}

func (f *fakeAPI) PageStatus(context.Context, string) (int, error) { return f.status, nil }

func (f *fakeAPI) ExpirePage(context.Context, string) (*api.ExpirePageResponse, error) {
	return f.expire, nil
}

func (f *fakeAPI) DeletePage(_ context.Context, slug string) error {
	f.deleted = append(f.deleted, slug)
	return nil
}

func (f *fakeAPI) CreateKey(context.Context, string, string) (*api.CreateKeyResponse, error) {
	return f.created, nil
}

func (f *fakeAPI) ListKeys(context.Context) ([]api.Key, error) { return f.keys, nil }

func (f *fakeAPI) RevokeKey(context.Context, int64) error { return nil }

func (f *fakeAPI) Stats(context.Context) (*api.StatsResponse, error) { return f.stats, nil }

func (f *fakeAPI) Cleanup(context.Context) (*api.CleanupResponse, error) { return f.cleanup, nil }

func (f *fakeAPI) PageURL(slug string) string { return "https://flyer.example/" + slug }

func newTestCommand(fake apiClient) (*command, *bytes.Buffer, *bytes.Buffer) {
	out, errOut := &bytes.Buffer{}, &bytes.Buffer{}
	return &command{
		cfgLoaded: true,
		out:       out,
		errOut:    errOut,
		newClient: func(string, string, string) apiClient { return fake },
	}, out, errOut
}

func TestListJSONAndTextShareOneModel(t *testing.T) {
	expires := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	fake := &fakeAPI{pages: &api.PageListResponse{
		Pages: []api.Page{{Slug: "Ab3dE9xZ", Title: "Report", Size: 42, ExpiresAt: &expires}},
		Total: 1,
	}}
	c, out, errOut := newTestCommand(fake)

	if err := cmdList(c, []string{"--json"}); err != nil {
		t.Fatal(err)
	}
	var got api.PageListResponse
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("decode JSON: %v", err)
	}
	if len(got.Pages) != 1 || got.Pages[0].Slug != "Ab3dE9xZ" || got.Pages[0].Size != 42 {
		t.Fatalf("unexpected JSON pages: %+v", got.Pages)
	}
	if got.Pages[0].ExpiresAt == nil || !got.Pages[0].ExpiresAt.Equal(expires) {
		t.Fatalf("expires = %v, want %v", got.Pages[0].ExpiresAt, expires)
	}
	if errOut.Len() != 0 {
		t.Fatalf("JSON wrote metadata to stderr: %q", errOut)
	}

	out.Reset()
	errOut.Reset()
	if err := cmdList(c, nil); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{"SLUG", "Ab3dE9xZ", "Report"} {
		if !strings.Contains(text, want) {
			t.Fatalf("text output missing %q: %q", want, text)
		}
	}
	if errOut.Len() != 0 {
		t.Fatalf("text wrote metadata for a single page: %q", errOut)
	}
}

func TestPublishSplitsURLAndMetadata(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	fake := &fakeAPI{publish: &api.PublishResponse{
		Slug: "Ab3dE9xZ", URL: "https://flyer.example/Ab3dE9xZ", ExpiresAt: &expires, Protected: true,
	}}
	c, out, errOut := newTestCommand(fake)
	path := filepath.Join(t.TempDir(), "report.html")
	if err := os.WriteFile(path, []byte("<h1>hi</h1>"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cmdPublish(c, []string{path, "--title", "My Report"}); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "https://flyer.example/Ab3dE9xZ\n" {
		t.Fatalf("stdout = %q", got)
	}
	for _, want := range []string{"slug: Ab3dE9xZ", "protected: yes"} {
		if !strings.Contains(errOut.String(), want) {
			t.Fatalf("stderr missing %q: %q", want, errOut)
		}
	}
	if fake.published == nil || fake.published.Title != "My Report" || fake.published.HTML != "<h1>hi</h1>" {
		t.Fatalf("publish intent not passed: %+v", fake.published)
	}
}
