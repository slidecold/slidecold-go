package slidecold

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
)

// Config is the shared config file written by `slidecold login`
// (the Go, Node and Python CLIs read the same file).
type Config struct {
	APIKey  string `json:"apiKey,omitempty"`
	BaseURL string `json:"baseUrl,omitempty"`
}

// ConfigPath returns the config file path, the same file the Node and
// Python CLIs use:
//
//  1. $XDG_CONFIG_HOME/slidecold/config.json when XDG_CONFIG_HOME is set;
//  2. ~/.config/slidecold/config.json when that directory exists (any OS);
//  3. on Windows, os.UserConfigDir()/slidecold/config.json (%AppData%\slidecold);
//  4. otherwise (Linux, macOS) ~/.config/slidecold/config.json.
//
// macOS deliberately uses ~/.config rather than ~/Library/Application Support
// so the Go, Node and Python CLIs share one file.
func ConfigPath() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "slidecold", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dot := filepath.Join(home, ".config", "slidecold")
	if fi, err := os.Stat(dot); (err == nil && fi.IsDir()) || runtime.GOOS != "windows" {
		return filepath.Join(dot, "config.json"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "slidecold", "config.json"), nil
}

// LoadConfig reads the config file. A missing file yields an empty Config.
func LoadConfig() (Config, error) {
	var cfg Config
	p, err := ConfigPath()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	return cfg, json.Unmarshal(data, &cfg)
}

// SaveConfig writes the config file with 0600 permissions. An empty Config
// removes the file.
func SaveConfig(cfg Config) error {
	p, err := ConfigPath()
	if err != nil {
		return err
	}
	if cfg == (Config{}) {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(p, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Chmod(p, 0o600) // WriteFile keeps the mode of an existing file
}
