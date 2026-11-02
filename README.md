# SlideCold Go SDK, CLI and MCP server

[![Go Reference](https://pkg.go.dev/badge/github.com/slidecold/slidecold-go.svg)](https://pkg.go.dev/github.com/slidecold/slidecold-go)
[![ci](https://github.com/slidecold/slidecold-go/actions/workflows/ci.yml/badge.svg)](https://github.com/slidecold/slidecold-go/actions/workflows/ci.yml)

The official Go client for the [SlideCold](https://slidecold.com) API (Instagram cold-DM outreach), plus:

- **`slidecold`**: a command-line interface for every API operation;
- **`slidecold-mcp`**: an MCP server that exposes every operation as a tool to Claude, Cursor and other MCP clients.

The SDK package uses only the Go standard library. Every operation, model and method is generated from
[`openapi.yaml`](openapi.yaml) (49 operations).

## Install

```sh
# SDK (Go >= 1.22)
go get github.com/slidecold/slidecold-go

# CLI (Go >= 1.22)
go install github.com/slidecold/slidecold-go/cmd/slidecold@latest

# MCP server (Go >= 1.25: it is a nested module because the MCP go-sdk needs Go 1.25)
go install github.com/slidecold/slidecold-go/cmd/slidecold-mcp@latest
```

Prebuilt `slidecold` and `slidecold-mcp` binaries for Linux, macOS and Windows (amd64 and arm64) are attached to each
[GitHub release](https://github.com/slidecold/slidecold-go/releases). They carry signed build provenance; verify a
download with the [GitHub CLI](https://cli.github.com):

```sh
gh attestation verify slidecold_0.1.0_darwin_arm64.tar.gz --repo slidecold/slidecold-go
```

## Authentication

Create an API key (`cs_...`) at <https://slidecold.com/settings>. The SDK, CLI and MCP server look for it in this order:

1. the explicit option or flag (`slidecold.WithAPIKey`, `--api-key`);
2. the `SLIDECOLD_API_KEY` environment variable;
3. the shared config file written by `slidecold login`: `{"apiKey": "cs_...", "baseUrl": "..."}`.

The base URL resolves the same way (`WithBaseURL` / `--base-url`, `SLIDECOLD_BASE_URL`, config `baseUrl`), defaulting to
`https://slidecold.com/api/v1`.

The config file is shared with the Node and Python CLIs. Its location ([`slidecold.ConfigPath`](https://pkg.go.dev/github.com/slidecold/slidecold-go#ConfigPath)):

| Condition | Path |
|---|---|
| `XDG_CONFIG_HOME` is set | `$XDG_CONFIG_HOME/slidecold/config.json` |
| `~/.config/slidecold/` exists (any OS) | `~/.config/slidecold/config.json` |
| Windows | `%AppData%\slidecold\config.json` (`os.UserConfigDir`) |
| Linux, macOS | `~/.config/slidecold/config.json` |

macOS uses `~/.config` rather than `~/Library/Application Support` so all three CLIs share one file. `slidecold login`
writes it with `0600` permissions.

## SDK

```go
package main

import (
	"context"
	"fmt"
	"log"

	"github.com/slidecold/slidecold-go"
)

func main() {
	client := slidecold.NewClient() // or NewClient(slidecold.WithAPIKey("cs_..."))
	ctx := context.Background()

	campaigns, err := client.Campaigns.List(ctx, &slidecold.ListCampaignsParams{FolderID: "root"})
	if err != nil {
		log.Fatal(err)
	}
	for _, c := range campaigns {
		stats, err := client.Campaigns.Stats(ctx, c.ID)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("%s: %d sent, %d%% replied\n", c.Name, stats.TotalSent, stats.ReplyRate)
	}

	created, err := client.Campaigns.Create(ctx, &slidecold.CreateCampaignRequest{
		Name:        "Spring outreach",
		AudienceIDs: []string{"aud_123"},
		AccountIDs:  []string{"acc_123"},
		Templates:   []slidecold.CampaignTemplateInput{{Body: "Hey {{firstName}}, quick question..."}},
		DailyLimit:  slidecold.Ptr(25), // optional fields are pointers
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("created", created.ID)

	csv, err := client.Audiences.Export(ctx, "aud_123") // text/csv as a string
	_ = csv

	// Background jobs return 202 {jobId}; login jobs can be awaited.
	job, err := client.Accounts.Create(ctx, &slidecold.CreateAccountRequest{
		Username: "my_ig", Password: "...", ProxyID: slidecold.Ptr("proxy_123"),
	})
	if err == nil {
		status, err := client.Accounts.WaitForLoginJob(ctx, job.JobID, 0) // polls LoginStatus every 2s
		fmt.Println(status, err)
	}
}
```

Services: `client.Campaigns`, `client.Audiences`, `client.Conversations`, `client.Accounts`, `client.Proxies`,
`client.Folders`. Every method takes a `context.Context`. Uploads take a filename and an `io.Reader`
(`client.Conversations.SendPhoto`, `client.Audiences.ImportMembersFile`). Any operation can also be called generically
with `client.Call(ctx, slidecold.OperationByID("getCampaignStats"), slidecold.Call{...})`.

Options: `WithAPIKey`, `WithBaseURL`, `WithHTTPClient`, `WithMaxRetries` (default 2) and `WithTimeout` (per attempt,
default 2 minutes). Only `GET` requests are retried, on 429, 5xx and network errors, with exponential backoff (and
`Retry-After` when present). Requests send `User-Agent: slidecold-go/<version>`.

## Scopes

Each API key carries scopes `<resource>:<read|write>` for `accounts`, `audiences`, `campaigns` and `conversations`;
`:write` also grants `:read`. Proxies use the `accounts` scopes, and folders use the scope of their type. Every method's
doc comment, `slidecold <resource> <action> --help`, and every MCP tool description name the scope required.

## Errors

Non-2xx responses are returned as `*slidecold.Error` with `StatusCode`, `Message` (the API's `error` field) and the raw
`Body`. Helpers use `errors.As`:

```go
_, err := client.Campaigns.Create(ctx, req)
switch {
case slidecold.IsAuthError(err):       // 401: bad key, or no key configured (ErrNoAPIKey)
case slidecold.IsPermissionError(err): // 403: "This API key is missing the campaigns:write scope", or a plan limit
case slidecold.IsNotFound(err):        // 404
case slidecold.IsRateLimited(err):     // 429
}
var apiErr *slidecold.Error
if errors.As(err, &apiErr) {
	log.Println(apiErr.StatusCode, apiErr.Message)
}
```

## Webhooks

SlideCold signs webhook deliveries with the endpoint secret: the `X-SlideCold-Signature` header is the hex
HMAC-SHA256 of the raw request body. Verify it before parsing:

```go
http.HandleFunc("/webhooks/slidecold", func(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if !slidecold.VerifyWebhookSignature(body, r.Header.Get("X-SlideCold-Signature"), os.Getenv("SLIDECOLD_WEBHOOK_SECRET")) {
		http.Error(w, "bad signature", http.StatusUnauthorized)
		return
	}
	// json.Unmarshal(body, &event) ...
})
```

## CLI

```text
slidecold <resource> <action> [args] [--param value] [--data JSON|@file|@-] [--file path]
```

```sh
slidecold login                                  # prompts for the key (or: slidecold login --key cs_...)
slidecold campaigns list --folder-id root
slidecold campaigns stats cmp_123
slidecold campaigns create --data @campaign.json
slidecold campaigns update cmp_123 --data '{"status":"PAUSED"}'
slidecold audiences export aud_123 > members.csv  # raw CSV
slidecold audiences import-members aud_123 --file members.csv
slidecold conversations list --status replied --limit 50
slidecold conversations send-photo conv_123 --file photo.jpg
slidecold accounts login-status job_123
slidecold logout
```

Resources are `campaigns`, `audiences`, `conversations`, `accounts`, `proxies` and `folders`. `slidecold --help`,
`slidecold campaigns --help` and `slidecold campaigns create --help` (with endpoint, scope and the body's JSON
Schema) document the rest. Output is pretty-printed JSON (raw CSV for exports); errors go to stderr with exit code 1.
`--api-key` and `--base-url` override the configured values for one command.

## MCP server

`slidecold-mcp` is a stdio [MCP](https://modelcontextprotocol.io) server built on the official
[Go SDK](https://github.com/modelcontextprotocol/go-sdk). It registers one tool per operation (49), named after the
operationId in snake_case (`list_campaigns`, `get_campaign_stats`, `send_conversation_reply`, ...). Path and query
parameters are top-level arguments, the JSON request body is the `body` argument, and uploads take a local
`file_path`. `GET` tools are marked read-only; deletes and message sends are marked destructive. API errors come back
as tool errors so the model can see them.

**Claude Code**

```sh
claude mcp add slidecold -e SLIDECOLD_API_KEY=cs_... -- slidecold-mcp
```

**Claude Desktop** (`claude_desktop_config.json`) and **Cursor** (`~/.cursor/mcp.json` or `.cursor/mcp.json`):

```json
{
  "mcpServers": {
    "slidecold": {
      "command": "slidecold-mcp",
      "env": { "SLIDECOLD_API_KEY": "cs_..." }
    }
  }
}
```

Use the absolute path of the binary (`which slidecold-mcp`) if the client does not inherit your `PATH`. If you ran
`slidecold login`, the `env` block can be omitted.

## Development

```sh
go generate ./...            # regenerate operations_gen.go, models_gen.go, services_gen.go from openapi.yaml
go vet ./... && go test -race ./... ./cmd/slidecold-mcp/...
```

The repository is a Go workspace (`go.work`): the root module (SDK and CLI, Go 1.22) and `cmd/slidecold-mcp`
(Go 1.25). CI fails if the generated files are stale.

Releasing: push a `vX.Y.Z` tag; GoReleaser builds the binaries and GitHub attests them. To make
`go install .../cmd/slidecold-mcp@vX.Y.Z` work, once the root tag is on the module proxy run
`cd cmd/slidecold-mcp && GOWORK=off go get github.com/slidecold/slidecold-go@vX.Y.Z && GOWORK=off go mod tidy`, commit,
and push the tag `cmd/slidecold-mcp/vX.Y.Z`.

## License

MIT
