package slidecold

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func writeConfig(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestConfigResolution(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("SLIDECOLD_API_KEY", "")
	t.Setenv("SLIDECOLD_BASE_URL", "")

	// Nothing configured: production defaults.
	if c := NewClient(); c.apiKey != "" || c.BaseURL() != DefaultBaseURL {
		t.Fatalf("defaults: %q %q", c.apiKey, c.BaseURL())
	}

	writeConfig(t, filepath.Join(xdg, "slidecold", "config.json"), `{"apiKey":"cs_file","baseUrl":"http://file/api/v1/"}`)
	if c := NewClient(); c.apiKey != "cs_file" || c.BaseURL() != "http://file/api/v1" {
		t.Fatalf("config file: %q %q", c.apiKey, c.BaseURL())
	}

	t.Setenv("SLIDECOLD_API_KEY", "cs_env")
	t.Setenv("SLIDECOLD_BASE_URL", "http://env")
	if c := NewClient(); c.apiKey != "cs_env" || c.BaseURL() != "http://env" {
		t.Fatalf("env: %q %q", c.apiKey, c.BaseURL())
	}

	if c := NewClient(WithAPIKey("cs_opt"), WithBaseURL("http://opt")); c.apiKey != "cs_opt" || c.BaseURL() != "http://opt" {
		t.Fatalf("options: %q %q", c.apiKey, c.BaseURL())
	}
}

func TestConfigPathPrefersDotConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	dot := filepath.Join(home, ".config", "slidecold", "config.json")

	p, err := ConfigPath()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		ucd, _ := os.UserConfigDir()
		if want := filepath.Join(ucd, "slidecold", "config.json"); p != want {
			t.Errorf("windows without ~/.config/slidecold: %s, want %s", p, want)
		}
	} else if p != dot {
		t.Errorf("got %s, want %s", p, dot)
	}

	writeConfig(t, dot, `{}`)
	if p, _ := ConfigPath(); p != dot {
		t.Errorf("with ~/.config/slidecold: %s, want %s", p, dot)
	}
}

func TestSaveConfig(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := SaveConfig(Config{APIKey: "cs_x"}); err != nil {
		t.Fatal(err)
	}
	p, _ := ConfigPath()
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	if cfg, _ := LoadConfig(); cfg.APIKey != "cs_x" {
		t.Errorf("loaded %+v", cfg)
	}
	if err := SaveConfig(Config{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("empty config should remove the file, stat err = %v", err)
	}
}
