package slidecold

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(t *testing.T, h http.HandlerFunc, opts ...Option) *Client {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir()) // never read the developer's real config
	t.Setenv("SLIDECOLD_API_KEY", "")
	t.Setenv("SLIDECOLD_BASE_URL", "")
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient(append([]Option{WithAPIKey("cs_test"), WithBaseURL(srv.URL)}, opts...)...)
	c.retryBase = time.Millisecond
	return c
}

func TestAuthHeaderUserAgentAndDecode(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer cs_test" {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("User-Agent"); got != "slidecold-go/"+Version {
			t.Errorf("User-Agent = %q", got)
		}
		if r.Method != "GET" || r.URL.Path != "/campaigns" || r.URL.Query().Get("folderId") != "f1" || r.URL.Query().Get("archived") != "true" {
			t.Errorf("got %s %s", r.Method, r.URL)
		}
		_, _ = io.WriteString(w, `[{"id":"c1","name":"Spring","status":"ACTIVE","createdAt":"2026-01-02T03:04:05.000Z"}]`)
	})
	got, err := c.Campaigns.List(context.Background(), &ListCampaignsParams{FolderID: "f1", Archived: "true"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "c1" || got[0].Status != CampaignStatusActive || got[0].CreatedAt.Year() != 2026 {
		t.Fatalf("decoded %+v", got)
	}
}

func TestPathParamsAndJSONBody(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PATCH" || r.URL.EscapedPath() != "/campaigns/a%2Fb" {
			t.Errorf("got %s %s", r.Method, r.URL.EscapedPath())
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q", ct)
		}
		b, _ := io.ReadAll(r.Body)
		if string(b) != `{"status":"PAUSED","dailyLimit":0}` {
			t.Errorf("body = %s", b)
		}
		_, _ = io.WriteString(w, `{"id":"a/b","status":"PAUSED"}`)
	})
	st := CampaignStatusPaused
	got, err := c.Campaigns.Update(context.Background(), "a/b", &UpdateCampaignRequest{Status: &st, DailyLimit: Ptr(0)})
	if err != nil || got.Status != CampaignStatusPaused {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestErrorMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		is     func(error) bool
		msg    string
	}{
		{401, `{"error":"Invalid API key"}`, IsAuthError, "Invalid API key"},
		{403, `{"error":"This API key is missing the campaigns:write scope"}`, IsPermissionError, "This API key is missing the campaigns:write scope"},
		{404, `{"error":"Campaign not found"}`, IsNotFound, "Campaign not found"},
		{429, `{"error":"Rate limited"}`, IsRateLimited, "Rate limited"},
		{500, `oops`, func(err error) bool { return !IsAuthError(err) && !IsNotFound(err) }, "Internal Server Error"},
	}
	for _, tc := range cases {
		c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		})
		_, err := c.Campaigns.Create(context.Background(), &CreateCampaignRequest{Name: "x"})
		var e *Error
		if !errors.As(err, &e) {
			t.Fatalf("%d: want *Error, got %v", tc.status, err)
		}
		if e.StatusCode != tc.status || e.Message != tc.msg || string(e.Body) != tc.body || !tc.is(err) {
			t.Errorf("%d: got %+v", tc.status, e)
		}
		if tc.status == 401 && !strings.Contains(err.Error(), "https://slidecold.com/settings") {
			t.Errorf("401 message lacks settings URL: %v", err)
		}
	}
}

func TestNoAPIKey(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("SLIDECOLD_API_KEY", "")
	_, err := NewClient().Campaigns.List(context.Background(), nil)
	if !errors.Is(err, ErrNoAPIKey) || !IsAuthError(err) {
		t.Fatalf("got %v", err)
	}
}

func TestRetriesGETOnly(t *testing.T) {
	var n atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		w.WriteHeader(503)
	})
	if _, err := c.Campaigns.Stats(context.Background(), "c1"); err == nil {
		t.Fatal("want error")
	}
	if n.Load() != 3 {
		t.Errorf("GET attempts = %d, want 3 (1 + 2 retries)", n.Load())
	}
	n.Store(0)
	if _, err := c.Campaigns.Retry(context.Background(), "c1"); err == nil {
		t.Fatal("want error")
	}
	if n.Load() != 1 {
		t.Errorf("POST attempts = %d, want 1", n.Load())
	}

	// 429 then success; a client-side 4xx is not retried.
	n.Store(0)
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(429)
			return
		}
		_, _ = io.WriteString(w, `{"totalSent":5,"totalPending":0,"totalReplied":1,"totalFailed":0,"totalSkipped":0,"replyRate":17,"templates":[]}`)
	}, WithMaxRetries(1))
	st, err := c.Campaigns.Stats(context.Background(), "c1")
	if err != nil || st.ReplyRate != 17 || n.Load() != 2 {
		t.Fatalf("got %+v, %v after %d attempts", st, err, n.Load())
	}

	// Network errors (connection dropped) are retried for GET.
	n.Store(0)
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) < 3 {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
			return
		}
		_, _ = io.WriteString(w, `{"id":"a1"}`)
	})
	a, err := c.Accounts.Get(context.Background(), "a1")
	if err != nil || a.ID != "a1" || n.Load() != 3 {
		t.Fatalf("got %+v, %v after %d attempts", a, err, n.Load())
	}

	n.Store(0)
	c = testClient(t, func(w http.ResponseWriter, r *http.Request) { n.Add(1); w.WriteHeader(404) })
	_, _ = c.Campaigns.Get(context.Background(), "x")
	if n.Load() != 1 {
		t.Errorf("404 attempts = %d, want 1", n.Load())
	}
}

func TestCSVAndMultipart(t *testing.T) {
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/audiences/a1/export":
			w.Header().Set("Content-Type", "text/csv; charset=utf-8")
			_, _ = io.WriteString(w, "username,fullName\nbob,Bob\n")
		case "/conversations/v1/crm":
			w.Header().Set("Content-Type", "text/csv")
			_, _ = io.WriteString(w, "recipientUsername\nbob\n")
		case "/conversations/v1/photo":
			f, h, err := r.FormFile("photo")
			if err != nil {
				t.Fatal(err)
			}
			b, _ := io.ReadAll(f)
			if h.Filename != "pic.jpg" || string(b) != "JPEG" {
				t.Errorf("upload %q %q", h.Filename, b)
			}
			w.WriteHeader(202)
			_, _ = io.WriteString(w, `{"jobId":"j1"}`)
		}
	})
	ctx := context.Background()
	csv, err := c.Audiences.Export(ctx, "a1")
	if err != nil || csv != "username,fullName\nbob,Bob\n" {
		t.Fatalf("export = %q, %v", csv, err)
	}
	lead, err := c.Conversations.ExportLead(ctx, "v1", &ExportConversationLeadRequest{Format: Ptr("csv")})
	if err != nil || lead.CSV != "recipientUsername\nbob\n" {
		t.Fatalf("lead = %+v, %v", lead, err)
	}
	job, err := c.Conversations.SendPhoto(ctx, "v1", "pic.jpg", strings.NewReader("JPEG"))
	if err != nil || job.JobID != "j1" {
		t.Fatalf("photo = %+v, %v", job, err)
	}
}

func TestWaitForLoginJob(t *testing.T) {
	var n atomic.Int32
	c := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/accounts/login-status" || r.URL.Query().Get("jobId") != "j1" {
			t.Errorf("got %s", r.URL)
		}
		state := map[int32]string{1: "waiting", 2: "active"}[n.Add(1)]
		if state == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"state": "completed", "result": map[string]any{"ok": true}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": state})
	})
	st, err := c.Accounts.WaitForLoginJob(context.Background(), "j1", time.Millisecond)
	if err != nil || st.State != "completed" || n.Load() != 3 {
		t.Fatalf("got %+v, %v", st, err)
	}

	c = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"state":"failed","error":"bad password"}`)
	})
	if _, err := c.Accounts.WaitForLoginJob(context.Background(), "j1", time.Millisecond); err == nil || !strings.Contains(err.Error(), "bad password") {
		t.Fatalf("want failure, got %v", err)
	}
}

func TestOperationsTable(t *testing.T) {
	if len(Operations) != 49 {
		t.Fatalf("got %d operations, want 49", len(Operations))
	}
	seen := map[string]bool{}
	for _, op := range Operations {
		key := op.Resource + " " + op.Action
		if seen[op.ToolName] || seen[key] || op.Scope == "" || op.Summary == "" {
			t.Errorf("bad or duplicate operation %+v", op)
		}
		seen[op.ToolName], seen[key] = true, true
		if op.BodySchema != nil && !json.Valid(op.BodySchema) {
			t.Errorf("%s: invalid body schema", op.ID)
		}
	}
	if op := OperationByID("getCampaignStats"); op == nil || op.Action != "stats" || op.ToolName != "get_campaign_stats" {
		t.Errorf("getCampaignStats = %+v", op)
	}
}
