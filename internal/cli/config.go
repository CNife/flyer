package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type ClientConfig struct {
	Server   string `json:"server,omitempty"`
	Key      string `json:"key,omitempty"`
	AdminKey string `json:"admin_key,omitempty"`
}

func clientConfigPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "flyer", "config.json"), nil
}

func loadClientConfig() (ClientConfig, error) {
	path, err := clientConfigPath()
	if err != nil {
		return ClientConfig{}, err
	}
	//nolint:gosec // 路径来自本机用户配置目录，不是外部输入
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return ClientConfig{}, nil
	}
	if err != nil {
		return ClientConfig{}, err
	}
	var cfg ClientConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ClientConfig{}, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.Server = strings.TrimRight(cfg.Server, "/")
	return cfg, nil
}

func saveClientConfig(cfg ClientConfig) error {
	path, err := clientConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	cfg.Server = strings.TrimRight(cfg.Server, "/")
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func maskKey(key string) string {
	if key == "" {
		return "(not set)"
	}
	if len(key) <= 12 {
		return key[:4] + "..."
	}
	return key[:8] + "..." + key[len(key)-4:]
}
