package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/slidecold/slidecold-go"
)

func TestMCPServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer cs_test" {
			t.Errorf("auth = %q", r.Header.Get("Authorization"))
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /campaigns/c1/stats":
			_, _ = io.WriteString(w, `{"totalSent":3,"replyRate":33}`)
		case "PATCH /campaigns/c1":
			b, _ := io.ReadAll(r.Body)
			_, _ = w.Write(b) // echo the body
		case "POST /conversations/v1/photo":
			_, h, err := r.FormFile("photo")
			if err != nil || h.Filename != "pic.jpg" {
				t.Errorf("photo upload: %v", err)
			}
			w.WriteHeader(202)
			_, _ = io.WriteString(w, `{"jobId":"j1"}`)
		case "GET /campaigns":
			if r.URL.Query().Get("archived") != "true" {
				t.Errorf("query = %s", r.URL.RawQuery)
			}
			w.WriteHeader(403)
			_, _ = io.WriteString(w, `{"error":"This API key is missing the campaigns:read scope"}`)
		default:
			w.WriteHeader(404)
			_, _ = io.WriteString(w, `{"error":"Not found"}`)
		}
	}))
	defer srv.Close()

	ctx := context.Background()
	server := newServer(slidecold.NewClient(slidecold.WithAPIKey("cs_test"), slidecold.WithBaseURL(srv.URL)))
	st, ct := mcp.NewInMemoryTransports()
	if _, err := server.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != len(slidecold.Operations) || len(tools.Tools) != 49 {
		t.Fatalf("got %d tools", len(tools.Tools))
	}
	byName := map[string]*mcp.Tool{}
	for _, tl := range tools.Tools {
		byName[tl.Name] = tl
	}
	if tl := byName["get_campaign_stats"]; tl == nil || !tl.Annotations.ReadOnlyHint || !strings.Contains(tl.Description, "campaigns:read") {
		t.Fatalf("get_campaign_stats = %+v", tl)
	}
	if tl := byName["delete_campaign"]; tl == nil || tl.Annotations.ReadOnlyHint || tl.Annotations.DestructiveHint == nil || !*tl.Annotations.DestructiveHint {
		t.Fatalf("delete_campaign annotations = %+v", tl.Annotations)
	}
	if tl := byName["create_campaign"]; *tl.Annotations.DestructiveHint {
		t.Fatal("create_campaign should not be destructive")
	}
	schema, _ := json.Marshal(byName["send_conversation_photo"].InputSchema)
	if !strings.Contains(string(schema), `"file_path"`) || !strings.Contains(string(schema), `"required":["id","file_path"]`) {
		t.Fatalf("photo schema = %s", schema)
	}

	call := func(name string, args map[string]any) (string, bool) {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return res.Content[0].(*mcp.TextContent).Text, res.IsError
	}
	if text, isErr := call("get_campaign_stats", map[string]any{"id": "c1"}); isErr || !strings.Contains(text, `"replyRate": 33`) {
		t.Fatalf("stats: %q %v", text, isErr)
	}
	if text, isErr := call("update_campaign", map[string]any{"id": "c1", "body": map[string]any{"status": "PAUSED"}}); isErr || !strings.Contains(text, `"PAUSED"`) {
		t.Fatalf("update: %q %v", text, isErr)
	}
	pic := filepath.Join(t.TempDir(), "pic.jpg")
	_ = os.WriteFile(pic, []byte("JPEG"), 0o600)
	if text, isErr := call("send_conversation_photo", map[string]any{"id": "v1", "file_path": pic}); isErr || !strings.Contains(text, "j1") {
		t.Fatalf("photo: %q %v", text, isErr)
	}
	if text, isErr := call("list_campaigns", map[string]any{"archived": "true"}); !isErr || !strings.Contains(text, "campaigns:read") {
		t.Fatalf("403 should be a tool error: %q %v", text, isErr)
	}
	if text, isErr := call("get_campaign", map[string]any{"id": "missing"}); !isErr || !strings.Contains(text, "404") {
		t.Fatalf("404 should be a tool error: %q %v", text, isErr)
	}
}
