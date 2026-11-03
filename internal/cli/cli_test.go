package cli

import (
	"flag"
	"reflect"
	"testing"
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
		{"0", 0, true},
		{"-1", 0, true},
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
