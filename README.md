# SlideCold Go SDK

[![Go Reference](https://pkg.go.dev/badge/github.com/slidecold/slidecold-go.svg)](https://pkg.go.dev/github.com/slidecold/slidecold-go)
[![ci](https://github.com/slidecold/slidecold-go/actions/workflows/ci.yml/badge.svg)](https://github.com/slidecold/slidecold-go/actions/workflows/ci.yml)

The official Go client for the [SlideCold](https://slidecold.com) API (Instagram cold-DM outreach).

The package uses only the Go standard library. Every operation, model and method is generated from
[`openapi.yaml`](openapi.yaml) (49 operations).

Looking for the command line or an MCP server?

- **CLI**: [slidecold/slidecold-cli](https://github.com/slidecold/slidecold-cli) (`slidecold`);
- **MCP server**: [slidecold/slidecold-mcp](https://github.com/slidecold/slidecold-mcp).

Both moved out of this repository in v0.2.0.

## Install

```sh
go get github.com/slidecold/slidecold-go   # Go >= 1.22
```

There are no binaries to download. Go verifies every module version against the
[checksum database](https://sum.golang.org) (`go.sum`), so no separate signature or attestation is needed.

## Authentication

Create an API key (`cs_...`) at <https://slidecold.com/settings>. The SDK looks for it in this order:

1. the explicit option (`slidecold.WithAPIKey`);
2. the `SLIDECOLD_API_KEY` environment variable;
3. the shared config file written by [`slidecold login`](https://github.com/slidecold/slidecold-cli): `{"apiKey": "cs_...", "baseUrl": "..."}`.

The base URL resolves the same way (`WithBaseURL`, `SLIDECOLD_BASE_URL`, config `baseUrl`), defaulting to
`https://slidecold.com/api/v1`.

The config file is shared with the CLI and the other SDKs; read and write it with `LoadConfig` and `SaveConfig`. Its location ([`slidecold.ConfigPath`](https://pkg.go.dev/github.com/slidecold/slidecold-go#ConfigPath)):

| Condition | Path |
|---|---|
| `XDG_CONFIG_HOME` is set | `$XDG_CONFIG_HOME/slidecold/config.json` |
| `~/.config/slidecold/` exists (any OS) | `~/.config/slidecold/config.json` |
| Windows | `%AppData%\slidecold\config.json` (`os.UserConfigDir`) |
| Linux, macOS | `~/.config/slidecold/config.json` |

macOS uses `~/.config` rather than `~/Library/Application Support` so every SlideCold tool shares one file. `SaveConfig`
(used by `slidecold login`) writes it with `0600` permissions.

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
doc comment names the scope required.

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

## Development

```sh
go generate ./...            # regenerate operations_gen.go, models_gen.go, services_gen.go from openapi.yaml
go vet ./... && go test -race ./...
```

CI fails if the generated files are stale.

Releasing: push a `vX.Y.Z` tag (and bump `Version` in `slidecold.go` first). The release workflow runs the tests,
creates a GitHub release with generated notes, and asks proxy.golang.org to fetch the tag so pkg.go.dev lists it.

## License

MIT
