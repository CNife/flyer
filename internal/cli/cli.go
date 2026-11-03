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
	"os/signal"
	"path/filepath"
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
  flyer delete <slug>              delete a page you own
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

Publish examples:
  flyer publish report.html
  flyer publish report.html --ttl 24h
  flyer publish report.html --permanent
  flyer publish report.html --password secret
  cat report.html | flyer publish -
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
	c := client.New(resolveServer(*serverFlag, cfg), resolvePublishKey(*keyFlag, cfg), "")
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

func cmdDelete(args []string) error {
	fs := newFlagSet("delete")
	serverFlag := fs.String("server", "", "server URL")
	keyFlag := fs.String("key", "", "API key")
	if err := parseFlags(fs, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: flyer delete <slug>")
	}
	cfg, err := loadClientConfig()
	if err != nil {
		return err
	}
	c := client.New(resolveServer(*serverFlag, cfg), resolvePublishKey(*keyFlag, cfg), "")
	if err := c.DeletePage(context.Background(), fs.Arg(0)); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "deleted %s\n", fs.Arg(0))
	return nil
}

func cmdAdmin(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: flyer admin key|stats|cleanup")
	}
	switch args[0] {
	case "key", "keys":
		return cmdAdminKey(args[1:])
	case "stats":
		return cmdAdminStats(args[1:])
	case "cleanup":
		return cmdAdminCleanup(args[1:])
	default:
		return fmt.Errorf("unknown admin command %q", args[0])
	}
}

func cmdAdminKey(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: flyer admin key create|list|revoke")
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
	if len(args) == 0 {
		return errors.New("usage: flyer config set server|key|admin-key <value> | show | path")
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
	defer st.Close()
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

func resolvePublishKey(flagValue string, cfg ClientConfig) string {
	if flagValue != "" {
		return flagValue
	}
	if v := os.Getenv("FLYER_KEY"); v != "" {
		return v
	}
	if cfg.Key != "" {
		return cfg.Key
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
		if secs <= 0 {
			return 0, errors.New("ttl must be positive")
		}
		return secs, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid ttl %q", value)
	}
	if d <= 0 {
		return 0, errors.New("ttl must be positive")
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
