// Command slidecold-mcp is a stdio MCP server exposing every SlideCold API
// operation as a tool. It reads the API key like the SDK does
// (SLIDECOLD_API_KEY, then the shared config file).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/slidecold/slidecold-go"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "version") {
		fmt.Println("slidecold-mcp", version)
		return
	}
	if err := newServer(slidecold.NewClient()).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}

// version is set by goreleaser with -ldflags "-X main.version=...".
var version = slidecold.Version

func newServer(client *slidecold.Client) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "slidecold", Title: "SlideCold", Version: version}, nil)
	for _, op := range slidecold.Operations {
		s.AddTool(tool(op), handler(client, op))
	}
	return s
}

func tool(op *slidecold.Operation) *mcp.Tool {
	props := map[string]json.RawMessage{}
	var order []string
	required := []string{}
	add := func(name string, schema json.RawMessage, req bool) {
		props[name] = schema
		order = append(order, name)
		if req {
			required = append(required, name)
		}
	}
	for _, p := range op.PathParams {
		add(p, json.RawMessage(`{"type":"string"}`), true)
	}
	for _, q := range op.QueryParams {
		add(q.Name, q.Schema, q.Required)
	}
	if op.BodySchema != nil {
		// A body that can also be a file upload is optional here.
		add("body", op.BodySchema, op.BodyRequired && op.FileField == "")
	}
	if op.FileField != "" {
		desc := fmt.Sprintf("Path of a local file to upload as the multipart %q field.", op.FileField)
		add("file_path", json.RawMessage(fmt.Sprintf(`{"type":"string","description":%q}`, desc)), op.BodySchema == nil)
	}
	var b bytes.Buffer
	b.WriteString(`{"type":"object","properties":{`)
	for i, n := range order {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, "%q:%s", n, props[n])
	}
	req, _ := json.Marshal(required)
	fmt.Fprintf(&b, `},"required":%s,"additionalProperties":false}`, req)

	desc := op.Summary + "."
	if op.Description != "" {
		desc += "\n\n" + op.Description
	}
	desc += fmt.Sprintf("\n\nRequired API key scope: %s. Endpoint: %s %s.", op.Scope, op.HTTPMethod, op.Path)
	readOnly := op.HTTPMethod == "GET"
	ann := &mcp.ToolAnnotations{Title: op.Summary, ReadOnlyHint: readOnly}
	if !readOnly {
		destructive := op.Destructive
		ann.DestructiveHint = &destructive
	}
	return &mcp.Tool{
		Name:        op.ToolName,
		Title:       op.Summary,
		Description: desc,
		InputSchema: json.RawMessage(b.Bytes()),
		Annotations: ann,
	}
}

func handler(client *slidecold.Client, op *slidecold.Operation) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, err := invoke(ctx, client, op, req.Params.Arguments)
		if err != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: res}}}, nil
	}
}

func invoke(ctx context.Context, client *slidecold.Client, op *slidecold.Operation, raw json.RawMessage) (string, error) {
	args := map[string]json.RawMessage{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &args); err != nil {
			return "", fmt.Errorf("arguments must be a JSON object: %w", err)
		}
	}
	call := slidecold.Call{PathParams: map[string]string{}, Query: url.Values{}}
	for _, p := range op.PathParams {
		call.PathParams[p] = scalar(args[p])
	}
	for _, q := range op.QueryParams {
		if v, ok := args[q.Name]; ok {
			call.Query.Set(q.Name, scalar(v))
		}
	}
	if b, ok := args["body"]; ok && op.BodySchema != nil && string(b) != "null" {
		call.Body = b
	}
	if p := scalar(args["file_path"]); p != "" && op.FileField != "" {
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		defer f.Close()
		call.File = &slidecold.File{Name: filepath.Base(p), Reader: f}
	}
	resp, err := client.Call(ctx, op, call)
	if err != nil {
		if slidecold.IsPermissionError(err) {
			return "", fmt.Errorf("%w (this tool needs the %s scope, or a plan limit was reached)", err, op.Scope)
		}
		return "", err
	}
	if resp.IsCSV() || !json.Valid(resp.Body) {
		return string(resp.Body), nil
	}
	var out bytes.Buffer
	if err := json.Indent(&out, resp.Body, "", "  "); err != nil {
		return string(resp.Body), nil
	}
	return out.String(), nil
}

// scalar turns a JSON string, number or boolean into its query-string form.
func scalar(v json.RawMessage) string {
	var s string
	if json.Unmarshal(v, &s) == nil {
		return s
	}
	return strings.TrimSpace(string(v))
}
