// Package slidecold is the official Go client for the SlideCold API
// (https://slidecold.com), the Instagram cold-DM outreach platform.
//
// Create a client and call the resource services:
//
//	client := slidecold.NewClient() // key from SLIDECOLD_API_KEY or ~/.config/slidecold/config.json
//	campaigns, err := client.Campaigns.List(ctx, nil)
//
// Every operation of the API is also described in [Operations] and can be
// called generically with [Client.Call]. Operations, models and service
// methods are generated from openapi.yaml.
//
// The package uses only the standard library.
package slidecold

//go:generate go run ./internal/gen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Version is the SDK version, sent in the User-Agent header.
const Version = "0.2.0"

// DefaultBaseURL is the production API base URL.
const DefaultBaseURL = "https://slidecold.com/api/v1"

// Operation describes one API operation. The table is generated from the
// OpenAPI spec; see [Operations].
type Operation struct {
	ID          string // OpenAPI operationId, e.g. "getCampaignStats"
	Tag         string // OpenAPI tag, e.g. "Campaigns"
	Resource    string // CLI resource, e.g. "campaigns"
	Action      string // CLI action, e.g. "stats"
	GoMethod    string // service method, e.g. "Stats"
	ToolName    string // MCP tool name, e.g. "get_campaign_stats"
	HTTPMethod  string
	Path        string // relative to the base URL, with {param} placeholders
	Summary     string
	Description string
	Scope       string // API key scope, from x-required-scope
	PathParams  []string
	QueryParams []Param
	// BodySchema is the JSON Schema of the JSON request body, if any.
	BodySchema   json.RawMessage
	BodyRequired bool
	// FileField is the multipart form field for file uploads, if any.
	FileField string
	// CSV reports that the operation may answer with text/csv.
	CSV bool
	// Destructive reports a DELETE or an irreversible action (sending a DM).
	Destructive bool
}

// Param is a query parameter of an Operation.
type Param struct {
	Name        string
	Required    bool
	Description string
	Schema      json.RawMessage
}

// OperationByID returns the operation with the given operationId, or nil.
func OperationByID(id string) *Operation {
	for _, op := range Operations {
		if op.ID == id {
			return op
		}
	}
	return nil
}

// Call holds the inputs of a generic operation call made with [Client.Call].
type Call struct {
	PathParams map[string]string
	Query      url.Values
	// Body is JSON-encoded unless nil. A json.RawMessage is sent as is.
	Body any
	// File is sent as multipart/form-data in the operation's FileField.
	File *File
}

// File is a file upload for multipart operations.
type File struct {
	Name   string
	Reader io.Reader
}

// Response is the raw result of [Client.Call].
type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

// IsCSV reports whether the response body is text/csv.
func (r *Response) IsCSV() bool {
	mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return mt == "text/csv"
}

// Option configures a Client.
type Option func(*core)

// WithAPIKey sets the API key (cs_...).
func WithAPIKey(key string) Option { return func(c *core) { c.apiKey = key } }

// WithBaseURL sets the API base URL, e.g. "http://localhost:3000/api/v1".
func WithBaseURL(u string) Option { return func(c *core) { c.baseURL = strings.TrimRight(u, "/") } }

// WithHTTPClient sets the HTTP client used for requests.
func WithHTTPClient(h *http.Client) Option { return func(c *core) { c.http = h } }

// WithMaxRetries sets how many times a failed GET request (429, 5xx or a
// network error) is retried. The default is 2. Other methods never retry.
func WithMaxRetries(n int) Option { return func(c *core) { c.maxRetries = n } }

// WithTimeout sets the timeout of each request attempt. The default is 2 minutes.
func WithTimeout(d time.Duration) Option { return func(c *core) { c.timeout = d } }

type core struct {
	apiKey, baseURL string
	http            *http.Client
	maxRetries      int
	timeout         time.Duration
	retryBase       time.Duration
}

// NewClient returns a client. Without options, the API key comes from
// SLIDECOLD_API_KEY, then the config file (see [ConfigPath]); the base URL
// from SLIDECOLD_BASE_URL, then the config file, then [DefaultBaseURL].
// Options take precedence over both.
func NewClient(opts ...Option) *Client {
	cfg, _ := LoadConfig() // a missing or broken config file just means no defaults
	c := &Client{core: core{
		apiKey:     firstNonEmpty(os.Getenv("SLIDECOLD_API_KEY"), cfg.APIKey),
		baseURL:    strings.TrimRight(firstNonEmpty(os.Getenv("SLIDECOLD_BASE_URL"), cfg.BaseURL, DefaultBaseURL), "/"),
		http:       http.DefaultClient,
		maxRetries: 2,
		timeout:    2 * time.Minute,
		retryBase:  500 * time.Millisecond,
	}}
	for _, o := range opts {
		o(&c.core)
	}
	c.initServices()
	return c
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// BaseURL returns the base URL the client sends requests to.
func (c *Client) BaseURL() string { return c.baseURL }

// Call performs op with raw inputs and returns the raw response. Non-2xx
// responses are returned as *Error. The CLI and MCP server use this.
func (c *Client) Call(ctx context.Context, op *Operation, call Call) (*Response, error) {
	if c.apiKey == "" {
		return nil, ErrNoAPIKey
	}
	path := op.Path
	for _, p := range op.PathParams {
		v := call.PathParams[p]
		if v == "" {
			return nil, fmt.Errorf("slidecold: %s: missing path parameter %q", op.ID, p)
		}
		path = strings.ReplaceAll(path, "{"+p+"}", url.PathEscape(v))
	}
	for _, q := range op.QueryParams {
		if q.Required && call.Query.Get(q.Name) == "" {
			return nil, fmt.Errorf("slidecold: %s: missing query parameter %q", op.ID, q.Name)
		}
	}
	u := c.baseURL + path
	if len(call.Query) > 0 {
		u += "?" + call.Query.Encode()
	}

	var body []byte
	var contentType string
	switch {
	case call.File != nil:
		if op.FileField == "" {
			return nil, fmt.Errorf("slidecold: %s does not accept a file", op.ID)
		}
		// shortcut: the upload is buffered in memory; stream via io.Pipe if large files matter.
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		fw, err := mw.CreateFormFile(op.FileField, call.File.Name)
		if err != nil {
			return nil, err
		}
		if _, err := io.Copy(fw, call.File.Reader); err != nil {
			return nil, fmt.Errorf("slidecold: reading upload: %w", err)
		}
		if err := mw.Close(); err != nil {
			return nil, err
		}
		body, contentType = buf.Bytes(), mw.FormDataContentType()
	case call.Body != nil:
		var err error
		if body, err = json.Marshal(call.Body); err != nil {
			return nil, fmt.Errorf("slidecold: encoding body: %w", err)
		}
		contentType = "application/json"
	}

	attempts := 1
	if op.HTTPMethod == http.MethodGet && c.maxRetries > 0 {
		attempts += c.maxRetries
	}
	for i := 0; ; i++ {
		resp, err := c.attempt(ctx, op.HTTPMethod, u, body, contentType)
		if i+1 >= attempts || !retryable(resp, err) || ctx.Err() != nil {
			return resp, err
		}
		wait := c.retryBase << i
		wait += time.Duration(rand.Int64N(int64(wait)/2 + 1))
		if resp != nil {
			if s, perr := strconv.Atoi(resp.Header.Get("Retry-After")); perr == nil && s >= 0 && s <= 60 {
				wait = time.Duration(s) * time.Second
			}
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil, ctx.Err()
		case <-t.C:
		}
	}
}

func retryable(resp *Response, err error) bool {
	if err != nil {
		var se *Error
		if errors.As(err, &se) {
			return se.StatusCode == http.StatusTooManyRequests || se.StatusCode >= 500
		}
		var ne net.Error
		return errors.As(err, &ne) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF)
	}
	return false
}

func (c *Client) attempt(ctx context.Context, method, u string, body []byte, contentType string) (*Response, error) {
	if c.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.timeout)
		defer cancel()
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("User-Agent", "slidecold-go/"+Version)
	req.Header.Set("Accept", "application/json, text/csv")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	hr, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer hr.Body.Close()
	data, err := io.ReadAll(hr.Body)
	if err != nil {
		return nil, err
	}
	resp := &Response{StatusCode: hr.StatusCode, Header: hr.Header, Body: data}
	if hr.StatusCode < 200 || hr.StatusCode > 299 {
		return resp, newError(resp)
	}
	return resp, nil
}

// do calls op and decodes the response into out.
func (c *Client) do(ctx context.Context, op *Operation, call Call, out any) error {
	resp, err := c.Call(ctx, op, call)
	if err != nil {
		return err
	}
	if resp.IsCSV() {
		switch o := out.(type) {
		case *string:
			*o = string(resp.Body)
			return nil
		case interface{ setCSV(string) }:
			o.setCSV(string(resp.Body))
			return nil
		}
	}
	if s, ok := out.(*string); ok {
		*s = string(resp.Body)
		return nil
	}
	if len(bytes.TrimSpace(resp.Body)) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		return fmt.Errorf("slidecold: %s: decoding response: %w", op.ID, err)
	}
	return nil
}

// Ptr returns a pointer to v, for optional request fields.
func Ptr[T any](v T) *T { return &v }

// WaitForLoginJob polls LoginStatus every interval (default 2s) until the
// login job created by Create, Relogin or CheckSession finishes. A failed job
// returns the final status together with an error.
func (s *AccountsService) WaitForLoginJob(ctx context.Context, jobID string, interval time.Duration) (*GetLoginStatusResponse, error) {
	if interval <= 0 {
		interval = 2 * time.Second
	}
	for {
		st, err := s.LoginStatus(ctx, jobID)
		if err != nil {
			return nil, err
		}
		switch st.State {
		case "completed":
			return st, nil
		case "failed":
			return st, fmt.Errorf("slidecold: login job %s failed: %s", jobID, st.Error)
		case "unknown":
			return st, fmt.Errorf("slidecold: login job %s is unknown or expired", jobID)
		}
		t := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			t.Stop()
			return st, ctx.Err()
		case <-t.C:
		}
	}
}
