package config

import (
	"os"
	"testing"
)

func TestWriteNewAndLoad(t *testing.T) {
	dir := t.TempDir()
	cfg, err := WriteNew(dir, "https://example.com/", "127.0.0.1:9999")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.BaseURL != "https://example.com" || cfg.Listen != "127.0.0.1:9999" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if len(cfg.AdminKey) < 20 || cfg.SessionSecret == "" {
		t.Fatalf("missing generated secrets: %+v", cfg)
	}
	info, err := os.Stat(Path(dir))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %o", info.Mode().Perm())
	}
	if info, err := os.Stat(dir + "/pages"); err != nil || !info.IsDir() {
		t.Fatalf("pages dir missing: %v", err)
	}

	loaded, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.AdminKey != cfg.AdminKey || loaded.SessionSecret != cfg.SessionSecret {
		t.Fatal("secrets did not round-trip")
	}
	if loaded.DefaultTTL.Duration.String() != "1h0m0s" || loaded.MaxUploadBytes != DefaultMaxUpload {
		t.Fatalf("unexpected defaults: %+v", loaded)
	}

	if _, err := WriteNew(dir, "https://example.com", ""); err == nil {
		t.Fatal("WriteNew overwrote an existing config")
	}
}
