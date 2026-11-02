# Changelog

## 0.2.0

The repository now holds only the SDK.

- The `slidecold` CLI moved to [slidecold/slidecold-cli](https://github.com/slidecold/slidecold-cli):
  `go install github.com/slidecold/slidecold-cli/cmd/slidecold@latest`, or download a release binary there.
- The MCP server moved to [slidecold/slidecold-mcp](https://github.com/slidecold/slidecold-mcp). The Go
  `cmd/slidecold-mcp` module (tag `cmd/slidecold-mcp/v0.1.0`) is no longer maintained.
- Releases no longer ship binaries; the SDK API is unchanged.

## 0.1.0

Initial release.

- `slidecold` package: a client for all 49 operations of the SlideCold API, generated from `openapi.yaml`, with typed
  models and resource services (`Campaigns`, `Audiences`, `Conversations`, `Accounts`, `Proxies`, `Folders`).
- API key and base URL from options, `SLIDECOLD_API_KEY` / `SLIDECOLD_BASE_URL`, or the shared config file.
- `*Error` with `IsAuthError`, `IsPermissionError`, `IsNotFound` and `IsRateLimited`; GET retries with backoff.
- `Accounts.WaitForLoginJob` and `VerifyWebhookSignature`.
- `slidecold` CLI with `login`, `logout` and a command for every operation.
- `slidecold-mcp` stdio MCP server with one tool per operation.
