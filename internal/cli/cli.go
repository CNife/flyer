// Package cli implements the flyer command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/CNife/flyer/internal/api"
	"github.com/CNife/flyer/internal/client"
	"github.com/CNife/flyer/internal/config"
	"github.com/CNife/flyer/internal/server"
	"github.com/CNife/flyer/internal/store"
)

var Version = "dev"

const defaultServer = "https://flyer.cnife.cn"

func Run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}
	switch args[0] {
	case "publish", "pub":
		return cmdPublish(args[1:])
	case "list", "ls":
		return cmdList(args[1:])
	case "open":
		return cmdOpen(args[1:])
	case "expire":
		return cmdExpire(args[1:])
	case "delete", "rm":
		return cmdDelete(args[1:])
	case "admin":
		return cmdAdmin(args[1:])
	case "config":
		return cmdConfig(args[1:])
	case "init-server":
		return cmdInitServer(args[1:])
	case "serve":
		return cmdServe(args[1:])
	case "version", "--version", "-v":
		fmt.Println("flyer " + Version)
		return nil
	case "help", "--help", "-h":
		printUsage()
		return nil
	default:
		return fmt.Errorf("unknown command %q (try flyer help)", args[0])
	}
}

func printUsage() {
	fmt.Print(`flyer - publish HTML to a shareable URL

Usage:
  flyer publish [file] [flags]     publish a file (or stdin with -)
  flyer list [flags]               list pages you own
  flyer open <slug>                open a live page in the browser
  flyer expire <slug>...           expire pages now
  flyer delete <slug>...           delete pages you own
  flyer admin key create           create a publisher key
  flyer admin key list             list keys
  flyer admin key revoke <id>      revoke a key
  flyer admin stats                show server stats
  flyer admin cleanup              delete pages past the grace period
  flyer config set server <url>    set the server URL
  flyer config set key <key>       set the publisher key
  flyer config set admin-key <key> set the admin key
  flyer config show                show client config
  flyer init-server                generate a server config (server side)
  flyer serve                      run the server
  flyer version

Manage examples:
  flyer list
  flyer list --limit 50 --offset 50
  flyer open Ab3dE9xZ
  flyer expire Ab3dE9xZ
  flyer delete Ab3dE9xZ Xy12abCD

Publish examples:
  flyer publish report.html
  flyer publish report.html --ttl 24h
  flyer publish report.html --permanent
  flyer publish report.html --password secret
  flyer publish report.html --no-qr
  flyer publish report.html --json
  cat report.html | flyer publish -

Flags can appear before or after the file name. stdout is only the URL;
metadata and the QR code go to stderr. Use "flyer <command> --help" for flags.
`)
}

func isHelp(arg string) bool {
	return arg == "help" || arg == "--help" || arg == "-h"
}

func printAdminUsage() {
	fmt.Print(`flyer admin - server administration

Usage:
  flyer admin key create --name <name> [--role publisher] [--json]
  flyer admin key list [--json]
  flyer admin key revoke <id>
  flyer admin stats [--json]
  flyer admin cleanup [--json]

All commands use the admin key from --admin-key, FLYER_ADMIN_KEY, or config.
`)
}

func printAdminKeyUsage() {
	fmt.Print(`flyer admin key - manage API keys

Usage:
  flyer admin key create --name <name> [--role publisher] [--json]
  flyer admin key list [--json]
  flyer admin key revoke <id>
`)
}

func printConfigUsage() {
	fmt.Print(`flyer config - client configuration

Usage:
  flyer config set server <url>
  flyer config set key <publisher-key>
  flyer config set admin-key <admin-key>
  flyer config show
  flyer config path

The config file is created with mode 0600.
`)
}

func cmdPublish(args []string) error {
	fs := newFlagSet("publish")
	serverFlag := fs.String("server", "", "server URL")
	keyFlag := fs.String("key", "", "API key")
	title := fs.String("title", "", "page title")
	ttl := fs.String("ttl", "", "time to live (e.g. 1h, 24h, 3600)")
	permanent := fs.Bool("permanent", false, "never expire")
	password := fs.String("password", "", "password protect the page")
	jsonOut := fs.Bool("json", false, "print JSON instead of a URL")
	forceQR := fs.Bool("qr", false, "print a terminal QR code")
	noQR := fs.Bool("no-qr", false, "do not print a QR code")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: flyer publish [file] [flags]

Publish an HTML file, or read it from stdin with "-".

Flags:
`)
		fs.PrintDefaults()
		fmt.Fprint(fs.Output(), `
Examples:
  flyer publish report.html
  flyer publish report.html --ttl 24h --no-qr
  flyer publish report.html --password secret
  flyer publish report.html --permanent --json
  cat report.html | flyer publish -
`)
	}
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *permanent && *ttl != "" {
		return errors.New("--permanent and --ttl are mutually exclusive")
	}
	cfg, err := loadClientConfig()
	if err != nil {
		return err
	}
	html, name, err := readHTML(fs.Args())
	if err != nil {
		return err
	}
	req := api.PublishRequest{
		HTML:      string(html),
		Title:     strings.TrimSpace(*title),
		Password:  *password,
		Permanent: *permanent,
	}
	if req.Password == "" {
		req.Password = os.Getenv("FLYER_PASSWORD")
	}
	// Adapter enrichment, not a publish rule: when the user did not pass
	// --title, infer one from the local file name. The server defaults a blank
	// title to the slug.
	if req.Title == "" && name != "stdin" && name != "-" {
		base := filepath.Base(name)
		req.Title = strings.TrimSuffix(base, filepath.Ext(base))
	}
	if *ttl != "" {
		secs, err := parseTTL(*ttl)
		if err != nil {
			return err
		}
		req.TTLSeconds = secs
	}
	c := client.New(resolveServer(*serverFlag, cfg), resolveAPIKey(*keyFlag, cfg), "")
	resp, err := c.Publish(context.Background(), req)
	if err != nil {
		return err
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}
	fmt.Fprintln(os.Stdout, resp.URL)
	fmt.Fprintf(os.Stderr, "slug: %s\n", resp.Slug)
	if resp.ExpiresAt == nil {
		fmt.Fprintln(os.Stderr, "expires: never")
	} else {
		fmt.Fprintf(os.Stderr, "expires: %s (%s)\n",
			resp.ExpiresAt.Local().Format("2006-01-02 15:04:05"),
			time.Until(*resp.ExpiresAt).Round(time.Minute))
	}
	fmt.Fprintf(os.Stderr, "protected: %s\n", yesNo(resp.Protected))
	if !*noQR && (*forceQR || isTerminal(os.Stderr)) {
		qrterminal.GenerateWithConfig(resp.URL, qrterminal.Config{
			Level:      qrterminal.L,
			Writer:     os.Stderr,
			HalfBlocks: true,
			QuietZone:  1,
		})
	}
	return nil
}

func cmdList(args []string) error {
	fs := newFlagSet("list")
	serverFlag := fs.String("server", "", "server URL")
	keyFlag := fs.String("key", "", "API key")
	limit := fs.Int("limit", 100, "maximum pages to return (1-500)")
	offset := fs.Int("offset", 0, "number of pages to skip")
	jsonOut := fs.Bool("json", false, "print JSON")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: flyer list [flags]

List pages uploaded by the configured API key. The admin key lists every page.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *limit < 1 || *limit > 500 {
		return errors.New("--limit must be between 1 and 500")
	}
	if *offset < 0 {
		return errors.New("--offset must not be negative")
	}
	cfg, err := loadClientConfig()
	if err != nil {
		return err
	}
	c := client.New(resolveServer(*serverFlag, cfg), resolveAPIKey(*keyFlag, cfg), "")
	list, err := c.ListPages(context.Background(), *limit, *offset)
	if err != nil {
		return err
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(list)
	}
	if len(list.Pages) == 0 {
		if list.Total == 0 {
			fmt.Fprintln(os.Stderr, "no pages")
		} else {
			fmt.Fprintf(os.Stderr, "no pages at offset %d (total %d)\n", *offset, list.Total)
		}
		return nil
	}
	now := time.Now()
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "SLUG\tTITLE\tSTATE\tEXPIRES\tSIZE")
	for _, p := range list.Pages {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\n",
			p.Slug, tableCell(p.Title), pageState(p, now), pageExpires(p), p.Size)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	end := *offset + len(list.Pages)
	if *offset > 0 || int64(end) < list.Total {
		fmt.Fprintf(os.Stderr, "showing %d-%d of %d", *offset+1, end, list.Total)
		if int64(end) < list.Total {
			fmt.Fprintf(os.Stderr, " (use --offset %d for more)", end)
		}
		fmt.Fprintln(os.Stderr)
	}
	return nil
}

func pageState(p api.Page, now time.Time) string {
	if p.ExpiresAt == nil {
		return "permanent"
	}
	if now.Before(*p.ExpiresAt) {
		return "active"
	}
	return "expired"
}

func pageExpires(p api.Page) string {
	if p.ExpiresAt == nil {
		return "never"
	}
	return p.ExpiresAt.Local().Format("2006-01-02 15:04")
}

func tableCell(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) > 40 {
		return string(runes[:39]) + "…"
	}
	return s
}

func cmdOpen(args []string) error {
	fs := newFlagSet("open")
	serverFlag := fs.String("server", "", "server URL")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: flyer open <slug> [flags]

Open a page in the default browser. Expired or missing pages are not opened.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: flyer open <slug>")
	}
	cfg, err := loadClientConfig()
	if err != nil {
		return err
	}
	slug := fs.Arg(0)
	c := client.New(resolveServer(*serverFlag, cfg), "", "")
	status, err := c.PageStatus(context.Background(), slug)
	if err != nil {
		return err
	}
	switch status {
	case http.StatusOK, http.StatusUnauthorized:
	case http.StatusGone:
		return errors.New("page has expired")
	case http.StatusNotFound:
		return errors.New("page not found")
	default:
		return fmt.Errorf("server returned %d", status)
	}
	pageURL := c.PageURL(slug)
	if err := openBrowser(pageURL); err != nil {
		return fmt.Errorf("open %s: %w", pageURL, err)
	}
	fmt.Fprintf(os.Stderr, "opened %s\n", pageURL)
	return nil
}

var openBrowser = func(url string) error {
	name, args := browserCommand(runtime.GOOS, url)
	//nolint:gosec // 命令名来自固定的 GOOS 分支，参数是用户自己配置的 server URL
	return exec.Command(name, args...).Start()
}

func browserCommand(goos, url string) (string, []string) {
	switch goos {
	case "darwin":
		return "open", []string{url}
	case "windows":
		return "rundll32", []string{"url.dll,FileProtocolHandler", url}
	default:
		return "xdg-open", []string{url}
	}
}

func cmdExpire(args []string) error {
	fs := newFlagSet("expire")
	serverFlag := fs.String("server", "", "server URL")
	keyFlag := fs.String("key", "", "API key")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: flyer expire <slug>... [flags]

Expire pages immediately. The admin key can expire any page.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: flyer expire <slug> [<slug> ...]")
	}
	cfg, err := loadClientConfig()
	if err != nil {
		return err
	}
	c := client.New(resolveServer(*serverFlag, cfg), resolveAPIKey(*keyFlag, cfg), "")
	var errs []error
	for _, slug := range fs.Args() {
		resp, err := c.ExpirePage(context.Background(), slug)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", slug, err))
			continue
		}
		fmt.Fprintf(os.Stderr, "expired %s at %s\n", slug,
			resp.ExpiresAt.Local().Format("2006-01-02 15:04:05"))
	}
	return errors.Join(errs...)
}

func cmdDelete(args []string) error {
	fs := newFlagSet("delete")
	serverFlag := fs.String("server", "", "server URL")
	keyFlag := fs.String("key", "", "API key")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), `Usage: flyer delete <slug>... [flags]

Flags:
`)
		fs.PrintDefaults()
	}
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() == 0 {
		return errors.New("usage: flyer delete <slug> [<slug> ...]")
	}
	cfg, err := loadClientConfig()
	if err != nil {
		return err
	}
	c := client.New(resolveServer(*serverFlag, cfg), resolveAPIKey(*keyFlag, cfg), "")
	var errs []error
	for _, slug := range fs.Args() {
		if err := c.DeletePage(context.Background(), slug); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", slug, err))
			continue
		}
		fmt.Fprintf(os.Stderr, "deleted %s\n", slug)
	}
	return errors.Join(errs...)
}

func cmdAdmin(args []string) error {
	if len(args) == 0 || isHelp(args[0]) {
		printAdminUsage()
		return nil
	}
	switch args[0] {
	case "key", "keys":
		return cmdAdminKey(args[1:])
	case "stats":
		return cmdAdminStats(args[1:])
	case "cleanup":
		return cmdAdminCleanup(args[1:])
	default:
		return fmt.Errorf("unknown admin command %q (try flyer admin --help)", args[0])
	}
}

func cmdAdminKey(args []string) error {
	if len(args) == 0 || isHelp(args[0]) {
		printAdminKeyUsage()
		return nil
	}
	switch args[0] {
	case "create":
		fs := newFlagSet("admin key create")
		serverFlag := fs.String("server", "", "server URL")
		adminKeyFlag := fs.String("admin-key", "", "admin key")
		name := fs.String("name", "", "key name")
		role := fs.String("role", "publisher", "key role (publisher)")
		jsonOut := fs.Bool("json", false, "print JSON")
		if err := parseFlags(fs, args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if strings.TrimSpace(*name) == "" {
			return errors.New("--name is required")
		}
		cfg, err := loadClientConfig()
		if err != nil {
			return err
		}
		c := client.New(resolveServer(*serverFlag, cfg), "", resolveAdminKey(*adminKeyFlag, cfg))
		resp, err := c.CreateKey(context.Background(), *name, *role)
		if err != nil {
			return err
		}
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(resp)
		}
		fmt.Fprintln(os.Stdout, resp.Key)
		fmt.Fprintf(os.Stderr, "id: %d\nname: %s\nrole: %s\n", resp.ID, resp.Name, resp.Role)
		return nil

	case "list":
		fs := newFlagSet("admin key list")
		serverFlag := fs.String("server", "", "server URL")
		adminKeyFlag := fs.String("admin-key", "", "admin key")
		jsonOut := fs.Bool("json", false, "print JSON")
		if err := parseFlags(fs, args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		cfg, err := loadClientConfig()
		if err != nil {
			return err
		}
		c := client.New(resolveServer(*serverFlag, cfg), "", resolveAdminKey(*adminKeyFlag, cfg))
		keys, err := c.ListKeys(context.Background())
		if err != nil {
			return err
		}
		if *jsonOut {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(api.KeyListResponse{Keys: keys})
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tNAME\tROLE\tCREATED\tSTATUS")
		for _, k := range keys {
			status := "active"
			if k.RevokedAt != nil {
				status = "revoked"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n", k.ID, k.Name, k.Role,
				k.CreatedAt.Local().Format("2006-01-02 15:04"), status)
		}
		return tw.Flush()

	case "revoke":
		fs := newFlagSet("admin key revoke")
		serverFlag := fs.String("server", "", "server URL")
		adminKeyFlag := fs.String("admin-key", "", "admin key")
		if err := parseFlags(fs, args[1:]); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil
			}
			return err
		}
		if fs.NArg() != 1 {
			return errors.New("usage: flyer admin key revoke <id>")
		}
		id, err := strconv.ParseInt(fs.Arg(0), 10, 64)
		if err != nil || id <= 0 {
			return errors.New("invalid key id")
		}
		cfg, err := loadClientConfig()
		if err != nil {
			return err
		}
		c := client.New(resolveServer(*serverFlag, cfg), "", resolveAdminKey(*adminKeyFlag, cfg))
		if err := c.RevokeKey(context.Background(), id); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "revoked key %d\n", id)
		return nil

	default:
		return fmt.Errorf("unknown key command %q", args[0])
	}
}

func cmdAdminStats(args []string) error {
	fs := newFlagSet("admin stats")
	serverFlag := fs.String("server", "", "server URL")
	adminKeyFlag := fs.String("admin-key", "", "admin key")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	cfg, err := loadClientConfig()
	if err != nil {
		return err
	}
	c := client.New(resolveServer(*serverFlag, cfg), "", resolveAdminKey(*adminKeyFlag, cfg))
	st, err := c.Stats(context.Background())
	if err != nil {
		return err
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(st)
	}
	fmt.Printf("pages: %d (active %d, expired %d, permanent %d)\n", st.Pages, st.Active, st.Expired, st.Permanent)
	fmt.Printf("bytes: %d\nkeys: %d\n", st.Bytes, st.Keys)
	return nil
}

func cmdAdminCleanup(args []string) error {
	fs := newFlagSet("admin cleanup")
	serverFlag := fs.String("server", "", "server URL")
	adminKeyFlag := fs.String("admin-key", "", "admin key")
	jsonOut := fs.Bool("json", false, "print JSON")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	cfg, err := loadClientConfig()
	if err != nil {
		return err
	}
	c := client.New(resolveServer(*serverFlag, cfg), "", resolveAdminKey(*adminKeyFlag, cfg))
	resp, err := c.Cleanup(context.Background())
	if err != nil {
		return err
	}
	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}
	fmt.Printf("deleted: %d\n", resp.Deleted)
	return nil
}

func cmdConfig(args []string) error {
	if len(args) == 0 || isHelp(args[0]) {
		printConfigUsage()
		return nil
	}
	cfg, err := loadClientConfig()
	if err != nil {
		return err
	}
	switch args[0] {
	case "path":
		path, err := clientConfigPath()
		if err != nil {
			return err
		}
		fmt.Println(path)
		return nil
	case "show":
		path, _ := clientConfigPath()
		fmt.Printf("server: %s\n", valueOr(cfg.Server, "(not set)"))
		fmt.Printf("key: %s\n", maskKey(cfg.Key))
		fmt.Printf("admin_key: %s\n", maskKey(cfg.AdminKey))
		fmt.Printf("path: %s\n", path)
		return nil
	case "set":
		if len(args) != 3 {
			return errors.New("usage: flyer config set server|key|admin-key <value>")
		}
		switch args[1] {
		case "server":
			u, err := url.Parse(args[2])
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return errors.New("server must be an absolute http(s) URL")
			}
			cfg.Server = strings.TrimRight(args[2], "/")
		case "key":
			if strings.TrimSpace(args[2]) == "" {
				return errors.New("key must not be empty")
			}
			cfg.Key = strings.TrimSpace(args[2])
		case "admin-key":
			if strings.TrimSpace(args[2]) == "" {
				return errors.New("admin key must not be empty")
			}
			cfg.AdminKey = strings.TrimSpace(args[2])
		default:
			return fmt.Errorf("unknown config field %q", args[1])
		}
		if err := saveClientConfig(cfg); err != nil {
			return err
		}
		fmt.Fprintln(os.Stderr, "saved")
		return nil
	default:
		return fmt.Errorf("unknown config command %q", args[0])
	}
}

func cmdInitServer(args []string) error {
	fs := newFlagSet("init-server")
	dir := fs.String("dir", "/opt/flyer", "data directory")
	baseURL := fs.String("base-url", config.DefaultBaseURL, "public base URL")
	listen := fs.String("listen", config.DefaultListen, "listen address")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	cfg, err := config.WriteNew(*dir, *baseURL, *listen)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, cfg.AdminKey)
	fmt.Fprintf(os.Stderr, "wrote %s\n", config.Path(*dir))
	return nil
}

func cmdServe(args []string) error {
	fs := newFlagSet("serve")
	dir := fs.String("dir", "/opt/flyer", "data directory")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	cfg, err := config.Load(*dir)
	if err != nil {
		return err
	}
	st, err := store.Open(*dir)
	if err != nil {
		return err
	}
	defer func() { _ = st.Close() }()
	logger := log.New(os.Stderr, "flyer: ", log.LstdFlags)
	srv := server.New(cfg, st, logger)
	httpSrv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv.StartCleanup(ctx, cfg.CleanupInterval.Duration)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
	}()
	logger.Printf("listening on %s, base URL %s", cfg.Listen, cfg.BaseURL)
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(fs.Output(), "Usage: flyer %s [flags]\n\nFlags:\n", name)
		fs.PrintDefaults()
	}
	return fs
}

// parseFlags accepts flags before or after positional arguments. The standard
// flag package stops at the first positional argument, which makes natural
// commands like "flyer publish page.html --ttl 1h" fail.
func parseFlags(fs *flag.FlagSet, args []string) error {
	var flagArgs, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(arg) > 1 && arg[0] == '-' {
			flagArgs = append(flagArgs, arg)
			name := strings.TrimLeft(arg, "-")
			if strings.Contains(name, "=") {
				continue
			}
			f := fs.Lookup(name)
			if f == nil {
				continue
			}
			if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
				continue
			}
			if i+1 < len(args) {
				flagArgs = append(flagArgs, args[i+1])
				i++
			}
		} else {
			positional = append(positional, arg)
		}
	}
	return fs.Parse(append(flagArgs, positional...))
}

func resolveServer(flagValue string, cfg ClientConfig) string {
	if flagValue != "" {
		return strings.TrimRight(flagValue, "/")
	}
	if v := os.Getenv("FLYER_SERVER"); v != "" {
		return strings.TrimRight(v, "/")
	}
	if cfg.Server != "" {
		return strings.TrimRight(cfg.Server, "/")
	}
	return defaultServer
}

func resolveAPIKey(flagValue string, cfg ClientConfig) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("FLYER_KEY"); v != "" {
		return v
	}
	if cfg.Key != "" {
		return cfg.Key
	}
	if v := os.Getenv("FLYER_ADMIN_KEY"); v != "" {
		return v
	}
	return cfg.AdminKey
}

func resolveAdminKey(flagValue string, cfg ClientConfig) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("FLYER_ADMIN_KEY"); v != "" {
		return v
	}
	return cfg.AdminKey
}

func parseTTL(value string) (int64, error) {
	if secs, err := strconv.ParseInt(value, 10, 64); err == nil {
		if secs < 0 {
			return 0, errors.New("ttl must not be negative")
		}
		return secs, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid ttl %q", value)
	}
	if d < 0 {
		return 0, errors.New("ttl must not be negative")
	}
	if d%time.Second != 0 {
		return 0, errors.New("ttl must be a whole number of seconds")
	}
	return int64(d.Seconds()), nil
}

func readHTML(args []string) ([]byte, string, error) {
	if len(args) > 1 {
		return nil, "", errors.New("usage: flyer publish [file]")
	}
	if len(args) == 1 && args[0] != "-" {
		data, err := os.ReadFile(args[0])
		if err != nil {
			return nil, "", err
		}
		return data, args[0], nil
	}
	if len(args) == 0 && isTerminal(os.Stdin) {
		return nil, "", errors.New("provide an HTML file or pipe HTML on stdin")
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		return nil, "", err
	}
	return data, "stdin", nil
}

func isTerminal(f *os.File) bool {
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}

func yesNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}
