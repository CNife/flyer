// Package config loads and generates the flyer server configuration.
package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

const (
	ConfigName       = "config.toml"
	DefaultListen    = "127.0.0.1:8787"
	DefaultBaseURL   = "https://flyer.cnife.cn"
	DefaultMaxUpload = int64(10 * 1024 * 1024)
)

// Duration is a TOML-friendly time.Duration ("1h", "15m").
type Duration struct {
	time.Duration
}

func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.Duration.String()), nil
}

type Config struct {
	Listen           string   `toml:"listen"`
	BaseURL          string   `toml:"base_url"`
	AdminKey         string   `toml:"admin_key"`
	SessionSecret    string   `toml:"session_secret"`
	DefaultTTL       Duration `toml:"default_ttl"`
	MaxTTL           Duration `toml:"max_ttl"`
	MaxUploadBytes   int64    `toml:"max_upload_bytes"`
	RateLimitPerHour int      `toml:"rate_limit_per_hour"`
	CleanupInterval  Duration `toml:"cleanup_interval"`
	GracePeriod      Duration `toml:"grace_period"`
}

func Default() Config {
	return Config{
		Listen:           DefaultListen,
		BaseURL:          DefaultBaseURL,
		DefaultTTL:       Duration{time.Hour},
		MaxTTL:           Duration{720 * time.Hour},
		MaxUploadBytes:   DefaultMaxUpload,
		RateLimitPerHour: 60,
		CleanupInterval:  Duration{15 * time.Minute},
		GracePeriod:      Duration{24 * time.Hour},
	}
}

func Path(dir string) string {
	return filepath.Join(dir, ConfigName)
}

func Load(dir string) (Config, error) {
	cfg := Default()
	path := Path(dir)
	if _, err := toml.DecodeFile(path, &cfg); err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	applyDefaults(&cfg)
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid %s: %w", path, err)
	}
	return cfg, nil
}

func applyDefaults(c *Config) {
	d := Default()
	if c.Listen == "" {
		c.Listen = d.Listen
	}
	if c.BaseURL == "" {
		c.BaseURL = d.BaseURL
	}
	if c.DefaultTTL.Duration <= 0 {
		c.DefaultTTL = d.DefaultTTL
	}
	if c.MaxTTL.Duration <= 0 {
		c.MaxTTL = d.MaxTTL
	}
	if c.MaxUploadBytes <= 0 {
		c.MaxUploadBytes = d.MaxUploadBytes
	}
	if c.RateLimitPerHour <= 0 {
		c.RateLimitPerHour = d.RateLimitPerHour
	}
	if c.CleanupInterval.Duration <= 0 {
		c.CleanupInterval = d.CleanupInterval
	}
	if c.GracePeriod.Duration <= 0 {
		c.GracePeriod = d.GracePeriod
	}
}

func (c Config) Validate() error {
	u, err := url.Parse(c.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("base_url must be an absolute http(s) URL")
	}
	if c.Listen == "" {
		return errors.New("listen must not be empty")
	}
	if !strings.HasPrefix(c.AdminKey, "fly_") || len(c.AdminKey) < 20 {
		return errors.New("admin_key must be a generated fly_ key")
	}
	if len(c.SessionSecret) < 32 {
		return errors.New("session_secret must be at least 32 characters")
	}
	if c.MaxUploadBytes <= 0 {
		return errors.New("max_upload_bytes must be positive")
	}
	if c.MaxTTL.Duration < c.DefaultTTL.Duration {
		return errors.New("max_ttl must be >= default_ttl")
	}
	return nil
}

// WriteNew creates dir/config.toml with fresh secrets. It never overwrites an
// existing config.
func WriteNew(dir, baseURL, listen string) (Config, error) {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	if listen == "" {
		listen = DefaultListen
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(abs, 0o750); err != nil {
		return Config{}, fmt.Errorf("create %s: %w", abs, err)
	}
	path := Path(abs)
	if _, err := os.Stat(path); err == nil {
		return Config{}, fmt.Errorf("%s already exists (refusing to overwrite)", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return Config{}, err
	}

	adminKey, err := RandomToken("fly_", 32)
	if err != nil {
		return Config{}, err
	}
	secret, err := RandomToken("", 32)
	if err != nil {
		return Config{}, err
	}
	cfg := Default()
	cfg.BaseURL = strings.TrimRight(baseURL, "/")
	cfg.Listen = listen
	cfg.AdminKey = adminKey
	cfg.SessionSecret = secret
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}

	body := fmt.Sprintf(`# flyer server configuration
# Keep this file private: it contains the admin key and cookie secret.
listen = %q
base_url = %q
admin_key = %q
session_secret = %q

default_ttl = %q
max_ttl = %q
max_upload_bytes = %d
rate_limit_per_hour = %d
cleanup_interval = %q
grace_period = %q
`,
		cfg.Listen, cfg.BaseURL, cfg.AdminKey, cfg.SessionSecret,
		cfg.DefaultTTL.Duration.String(), cfg.MaxTTL.Duration.String(),
		cfg.MaxUploadBytes, cfg.RateLimitPerHour,
		cfg.CleanupInterval.Duration.String(), cfg.GracePeriod.Duration.String(),
	)
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return Config{}, fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		return Config{}, err
	}
	if err := f.Close(); err != nil {
		return Config{}, err
	}
	if err := os.MkdirAll(filepath.Join(abs, "pages"), 0o750); err != nil {
		return Config{}, fmt.Errorf("create pages dir: %w", err)
	}
	return cfg, nil
}

// RandomToken returns prefix + base64url(n random bytes).
func RandomToken(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}
