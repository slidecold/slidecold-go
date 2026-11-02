package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type result struct {
	code           int
	stdout, stderr string
}

func cmd(stdin string, args ...string) result {
	var out, errb bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(stdin), &out, &errb)
	return result{code, out.String(), errb.String()}
}

func TestCLIEndToEnd(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("SLIDECOLD_API_KEY", "")
	t.Setenv("SLIDECOLD_BASE_URL", "")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		switch {
		case auth == "Bearer cs_bad":
			w.WriteHeader(401)
			_, _ = io.WriteString(w, `{"error":"Invalid API key"}`)
		case auth == "Bearer cs_scoped":
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"error":"This API key is missing the campaigns:read scope"}`)
		case r.Method == "GET" && r.URL.Path == "/campaigns":
			if f := r.URL.Query().Get("folderId"); f != "" && f != "f1" {
				t.Errorf("folderId = %q", f)
			}
			_, _ = io.WriteString(w, `[{"id":"c1","name":"Spring"}]`)
		case r.Method == "POST" && r.URL.Path == "/campaigns":
			b, _ := io.ReadAll(r.Body)
			if string(b) != `{"name":"New"}` {
				t.Errorf("body = %s", b)
			}
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"id":"c2","name":"New"}`)
		case r.URL.Path == "/audiences/a1/export":
			w.Header().Set("Content-Type", "text/csv")
			_, _ = io.WriteString(w, "username\nbob\n")
		case r.URL.Path == "/accounts/login-status":
			_, _ = io.WriteString(w, `{"state":"`+r.URL.Query().Get("jobId")+`"}`)
		case r.URL.Path == "/audiences/a1/import":
			f, h, err := r.FormFile("file")
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(f)
			_, _ = io.WriteString(w, `{"imported":1,"skipped":0,"total":1,"file":"`+h.Filename+`","size":`+jsonInt(len(b))+`}`)
		default:
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":"Not found"}`)
		}
	}))
	defer srv.Close()
	base := []string{"--base-url", srv.URL}

	// login validates the key: 401 rejected, nothing saved.
	if r := cmd("", append(base, "login", "--key", "cs_bad")...); r.code != 1 || !strings.Contains(r.stderr, "invalid API key") {
		t.Fatalf("login bad: %+v", r)
	}
	// 403 means valid but scoped; key prompted from stdin.
	r := cmd("cs_scoped\n", append(base, "login")...)
	if r.code != 0 || !strings.Contains(r.stdout, "valid") {
		t.Fatalf("login scoped: %+v", r)
	}
	cfgPath := filepath.Join(xdg, "slidecold", "config.json")
	fi, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Errorf("config mode %v", fi.Mode().Perm())
	}
	if r := cmd("", append(base, "login", "--key", "cs_good")...); r.code != 0 {
		t.Fatalf("login good: %+v", r)
	}
	var saved map[string]string
	data, _ := os.ReadFile(cfgPath)
	_ = json.Unmarshal(data, &saved)
	if saved["apiKey"] != "cs_good" || saved["baseUrl"] != srv.URL {
		t.Fatalf("saved config %s", data)
	}

	// Commands use the saved key and base URL; output is pretty JSON.
	r = cmd("", "campaigns", "list", "--folder-id", "f1")
	if r.code != 0 || r.stdout != "[\n  {\n    \"id\": \"c1\",\n    \"name\": \"Spring\"\n  }\n]\n" {
		t.Fatalf("list: %+v", r)
	}
	if r := cmd("", "campaigns", "create", "--data", `{"name":"New"}`); r.code != 0 || !strings.Contains(r.stdout, `"id": "c2"`) {
		t.Fatalf("create: %+v", r)
	}
	if r := cmd("", "audiences", "export", "a1"); r.code != 0 || r.stdout != "username\nbob\n" {
		t.Fatalf("export: %+v", r)
	}
	if r := cmd("", "accounts", "login-status", "completed"); r.code != 0 || !strings.Contains(r.stdout, `"completed"`) {
		t.Fatalf("login-status: %+v", r)
	}
	csv := filepath.Join(t.TempDir(), "members.csv")
	_ = os.WriteFile(csv, []byte("username\nbob\n"), 0o600)
	if r := cmd("", "audiences", "import-members", "a1", "--file", csv); r.code != 0 || !strings.Contains(r.stdout, `"file": "members.csv"`) {
		t.Fatalf("import file: %+v", r)
	}

	// Errors go to stderr with exit code 1.
	if r := cmd("", "campaigns", "get", "nope"); r.code != 1 || !strings.Contains(r.stderr, "404 Not found") || r.stdout != "" {
		t.Fatalf("404: %+v", r)
	}
	if r := cmd("", "--api-key", "cs_scoped", "campaigns", "list"); r.code != 1 || !strings.Contains(r.stderr, "campaigns:read") {
		t.Fatalf("403: %+v", r)
	}
	if r := cmd("", "campaigns", "stats"); r.code != 1 || !strings.Contains(r.stderr, "<id>") {
		t.Fatalf("missing arg: %+v", r)
	}
	if r := cmd("", "campaigns", "list", "--bogus", "1"); r.code != 1 || !strings.Contains(r.stderr, "unknown flag") {
		t.Fatalf("unknown flag: %+v", r)
	}

	if r := cmd("", "logout"); r.code != 0 {
		t.Fatalf("logout: %+v", r)
	}
	data, _ = os.ReadFile(cfgPath)
	if strings.Contains(string(data), "cs_good") {
		t.Fatalf("key still saved after logout: %s", data)
	}
}

func jsonInt(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestCLIHelp(t *testing.T) {
	if r := cmd("", "--help"); r.code != 0 || !strings.Contains(r.stdout, "campaigns") || !strings.Contains(r.stdout, "login") {
		t.Fatalf("help: %+v", r)
	}
	if r := cmd("", "campaigns", "--help"); r.code != 0 || !strings.Contains(r.stdout, "stats <id>") || !strings.Contains(r.stdout, "campaigns:read") {
		t.Fatalf("resource help: %+v", r)
	}
	r := cmd("", "help", "campaigns", "create")
	if r.code != 0 || !strings.Contains(r.stdout, "Required scope: campaigns:write") || !strings.Contains(r.stdout, `"templates"`) {
		t.Fatalf("action help: %+v", r)
	}
	if r := cmd("", "version"); r.code != 0 || !strings.HasPrefix(r.stdout, "slidecold ") {
		t.Fatalf("version: %+v", r)
	}
	if r := cmd("", "nope"); r.code != 1 {
		t.Fatalf("unknown: %+v", r)
	}
}
