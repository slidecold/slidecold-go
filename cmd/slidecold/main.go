// Command slidecold is the SlideCold command-line interface.
//
//	slidecold <resource> <action> [id...] [--param value] [--data JSON|@file] [--file path]
//
// Commands are generated from the operation table in package slidecold.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/slidecold/slidecold-go"
)

// version is set by goreleaser with -ldflags "-X main.version=...".
var version = slidecold.Version

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

type cli struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	flags          map[string]string // normalized name -> value
	pos            []string
	help           bool
}

// norm makes --folder-id, --folderId and --folderid equivalent.
func norm(s string) string { return strings.ToLower(strings.ReplaceAll(s, "-", "")) }

var valueless = map[string]bool{"help": true, "h": true}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	c := &cli{stdin: stdin, stdout: stdout, stderr: stderr, flags: map[string]string{}}
	if err := c.parse(args); err != nil {
		return c.fail(err)
	}
	if err := c.dispatch(ctx); err != nil {
		return c.fail(err)
	}
	return 0
}

func (c *cli) fail(err error) int {
	fmt.Fprintln(c.stderr, "Error:", strings.TrimPrefix(err.Error(), "slidecold: "))
	return 1
}

func (c *cli) parse(args []string) error {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			c.pos = append(c.pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			c.pos = append(c.pos, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		name = norm(name)
		if valueless[name] {
			c.help = true
			continue
		}
		if !hasVal {
			if i+1 >= len(args) {
				return fmt.Errorf("flag --%s needs a value", name)
			}
			i++
			val = args[i]
		}
		c.flags[name] = val
	}
	return nil
}

func (c *cli) take(name string) (string, bool) {
	v, ok := c.flags[name]
	delete(c.flags, name)
	return v, ok
}

func (c *cli) client() *slidecold.Client {
	var opts []slidecold.Option
	if k, ok := c.take("apikey"); ok {
		opts = append(opts, slidecold.WithAPIKey(k))
	}
	if u, ok := c.take("baseurl"); ok {
		opts = append(opts, slidecold.WithBaseURL(u))
	}
	return slidecold.NewClient(opts...)
}

func (c *cli) dispatch(ctx context.Context) error {
	if len(c.pos) == 0 {
		c.usage()
		return nil
	}
	switch c.pos[0] {
	case "help":
		c.pos = c.pos[1:]
		c.help = true
		return c.dispatch(ctx)
	case "version":
		fmt.Fprintln(c.stdout, "slidecold", version)
		return nil
	case "login":
		return c.login(ctx)
	case "logout":
		return c.logout()
	}
	ops := opsFor(c.pos[0])
	if ops == nil {
		return fmt.Errorf("unknown command %q (run `slidecold help`)", c.pos[0])
	}
	if len(c.pos) == 1 {
		c.resourceUsage(c.pos[0], ops)
		if c.help {
			return nil
		}
		return errors.New("missing action")
	}
	var op *slidecold.Operation
	for _, o := range ops {
		if o.Action == c.pos[1] {
			op = o
		}
	}
	if op == nil {
		return fmt.Errorf("unknown action %q for %s (run `slidecold %s --help`)", c.pos[1], c.pos[0], c.pos[0])
	}
	if c.help {
		c.opUsage(op)
		return nil
	}
	return c.call(ctx, op, c.pos[2:])
}

func opsFor(resource string) []*slidecold.Operation {
	var out []*slidecold.Operation
	for _, o := range slidecold.Operations {
		if o.Resource == resource {
			out = append(out, o)
		}
	}
	return out
}

// positional returns the operation's positional arguments: path params,
// then required query params.
func positional(op *slidecold.Operation) []string {
	names := append([]string(nil), op.PathParams...)
	for _, q := range op.QueryParams {
		if q.Required {
			names = append(names, q.Name)
		}
	}
	return names
}

func (c *cli) call(ctx context.Context, op *slidecold.Operation, args []string) error {
	client := c.client()
	names := positional(op)
	if len(args) != len(names) {
		return fmt.Errorf("%s %s takes %d argument(s) <%s>, got %d", op.Resource, op.Action, len(names), strings.Join(names, "> <"), len(args))
	}
	call := slidecold.Call{PathParams: map[string]string{}, Query: url.Values{}}
	for i, n := range names {
		if i < len(op.PathParams) {
			call.PathParams[n] = args[i]
		} else {
			call.Query.Set(n, args[i])
		}
	}
	for _, q := range op.QueryParams {
		if v, ok := c.take(norm(q.Name)); ok {
			call.Query.Set(q.Name, v)
		}
	}
	if d, ok := c.take("data"); ok {
		if op.BodySchema == nil {
			return fmt.Errorf("%s %s does not take --data", op.Resource, op.Action)
		}
		raw, err := c.readData(d)
		if err != nil {
			return err
		}
		if !json.Valid(raw) {
			return errors.New("--data is not valid JSON")
		}
		call.Body = json.RawMessage(raw)
	}
	if p, ok := c.take("file"); ok {
		if op.FileField == "" {
			return fmt.Errorf("%s %s does not take --file", op.Resource, op.Action)
		}
		f, err := os.Open(p)
		if err != nil {
			return err
		}
		defer f.Close()
		call.File = &slidecold.File{Name: filepath.Base(p), Reader: f}
	}
	for k := range c.flags {
		return fmt.Errorf("unknown flag --%s for %s %s", k, op.Resource, op.Action)
	}
	if op.BodyRequired && call.Body == nil && call.File == nil {
		if op.FileField != "" && op.BodySchema == nil {
			return fmt.Errorf("%s %s needs --file", op.Resource, op.Action)
		}
		return fmt.Errorf("%s %s needs --data (see `slidecold %s %s --help`)", op.Resource, op.Action, op.Resource, op.Action)
	}

	resp, err := client.Call(ctx, op, call)
	if err != nil {
		if slidecold.IsPermissionError(err) {
			return fmt.Errorf("%w (this command needs the %s scope, or a plan limit was reached)", err, op.Scope)
		}
		return err
	}
	return c.print(resp)
}

func (c *cli) readData(d string) ([]byte, error) {
	switch {
	case d == "@-":
		return io.ReadAll(c.stdin)
	case strings.HasPrefix(d, "@"):
		return os.ReadFile(d[1:])
	}
	return []byte(d), nil
}

func (c *cli) print(resp *slidecold.Response) error {
	if resp.IsCSV() || !json.Valid(resp.Body) {
		_, err := c.stdout.Write(resp.Body)
		return err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, resp.Body, "", "  "); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err := c.stdout.Write(buf.Bytes())
	return err
}

// ---------- login / logout ----------

func (c *cli) login(ctx context.Context) error {
	key, _ := c.take("key")
	baseURL, _ := c.take("baseurl")
	if key == "" {
		fmt.Fprintf(c.stdout, "Create an API key at %s, then paste it here.\nAPI key: ", slidecold.SettingsURL)
		line, err := bufio.NewReader(c.stdin).ReadString('\n')
		if err != nil && line == "" {
			return errors.New("no API key given")
		}
		key = strings.TrimSpace(line)
	}
	if !strings.HasPrefix(key, "cs_") {
		return errors.New("API keys start with cs_")
	}
	cfg, err := slidecold.LoadConfig()
	if err != nil {
		return fmt.Errorf("reading config: %w", err)
	}
	opts := []slidecold.Option{slidecold.WithAPIKey(key)}
	if baseURL != "" {
		opts = append(opts, slidecold.WithBaseURL(baseURL))
	}
	client := slidecold.NewClient(opts...)
	_, err = client.Campaigns.List(ctx, nil)
	switch {
	case slidecold.IsAuthError(err):
		return fmt.Errorf("invalid API key (create one at %s)", slidecold.SettingsURL)
	case slidecold.IsPermissionError(err):
		fmt.Fprintln(c.stdout, "Key is valid, but it has no campaigns:read scope; commands outside its scopes will fail with 403.")
	case err != nil:
		return fmt.Errorf("could not validate key: %w", err)
	}
	cfg.APIKey = key
	if baseURL != "" {
		cfg.BaseURL = baseURL
	}
	if err := slidecold.SaveConfig(cfg); err != nil {
		return err
	}
	p, _ := slidecold.ConfigPath()
	fmt.Fprintln(c.stdout, "Logged in. Key saved to", p)
	return nil
}

func (c *cli) logout() error {
	cfg, err := slidecold.LoadConfig()
	if err != nil {
		return err
	}
	cfg.APIKey = ""
	if err := slidecold.SaveConfig(cfg); err != nil {
		return err
	}
	fmt.Fprintln(c.stdout, "Logged out.")
	return nil
}

// ---------- help ----------

func (c *cli) usage() {
	w := c.stdout
	fmt.Fprintf(w, `slidecold %s - the SlideCold command line

Usage:
  slidecold <resource> <action> [args] [--param value] [--data JSON|@file|@-] [--file path]
  slidecold <resource> --help              list a resource's actions
  slidecold <resource> <action> --help     show an action's arguments and scope

Resources:
`, version)
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	seen := map[string]bool{}
	for _, o := range slidecold.Operations {
		if !seen[o.Resource] {
			seen[o.Resource] = true
			fmt.Fprintf(tw, "  %s\t%d actions\n", o.Resource, len(opsFor(o.Resource)))
		}
	}
	tw.Flush()
	fmt.Fprint(w, `
Other commands:
  login [--key cs_...]   save an API key to the config file
  logout                 remove the saved API key
  version                print the version
  help [command]         show help

Global flags:
  --api-key cs_...       API key (default: $SLIDECOLD_API_KEY, then the config file)
  --base-url URL         API base URL (default: $SLIDECOLD_BASE_URL, then the config file, then https://slidecold.com/api/v1)
`)
}

func (c *cli) resourceUsage(resource string, ops []*slidecold.Operation) {
	fmt.Fprintf(c.stdout, "Usage: slidecold %s <action> [args] [flags]\n\nActions:\n", resource)
	tw := tabwriter.NewWriter(c.stdout, 0, 0, 2, ' ', 0)
	for _, o := range ops {
		args := ""
		for _, n := range positional(o) {
			args += " <" + n + ">"
		}
		fmt.Fprintf(tw, "  %s%s\t%s\t[%s]\n", o.Action, args, o.Summary, o.Scope)
	}
	tw.Flush()
}

func (c *cli) opUsage(op *slidecold.Operation) {
	w := c.stdout
	args := ""
	for _, n := range positional(op) {
		args += " <" + n + ">"
	}
	fmt.Fprintf(w, "Usage: slidecold %s %s%s", op.Resource, op.Action, args)
	if op.BodySchema != nil {
		fmt.Fprint(w, " --data JSON|@file")
	}
	if op.FileField != "" {
		fmt.Fprint(w, " --file path")
	}
	fmt.Fprintf(w, "\n\n%s\n", op.Summary)
	if op.Description != "" {
		fmt.Fprintf(w, "\n%s\n", op.Description)
	}
	fmt.Fprintf(w, "\nEndpoint:       %s %s\nRequired scope: %s\n", op.HTTPMethod, op.Path, op.Scope)
	var flags []slidecold.Param
	for _, q := range op.QueryParams {
		if !q.Required {
			flags = append(flags, q)
		}
	}
	if len(flags) > 0 {
		fmt.Fprintln(w, "\nFlags:")
		for _, q := range flags {
			fmt.Fprintf(w, "  --%s\t%s %s\n", q.Name, q.Description, schemaHint(q.Schema))
		}
	}
	if op.BodySchema != nil {
		var pretty bytes.Buffer
		_ = json.Indent(&pretty, op.BodySchema, "  ", "  ")
		fmt.Fprintf(w, "\nRequest body (--data) JSON Schema:\n  %s\n", pretty.String())
	}
}

func schemaHint(s json.RawMessage) string {
	var v struct {
		Enum []any `json:"enum"`
		Type any   `json:"type"`
	}
	_ = json.Unmarshal(s, &v)
	if len(v.Enum) > 0 {
		return fmt.Sprintf("(one of %v)", v.Enum)
	}
	if v.Type != nil {
		return fmt.Sprintf("(%v)", v.Type)
	}
	return ""
}
