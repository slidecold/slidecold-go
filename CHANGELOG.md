# Changelog

## 0.1.0 (unreleased)

Initial release.

- `slidecold` package: a client for all 49 operations of the SlideCold API, generated from `openapi.yaml`, with typed
  models and resource services (`Campaigns`, `Audiences`, `Conversations`, `Accounts`, `Proxies`, `Folders`).
- API key and base URL from options, `SLIDECOLD_API_KEY` / `SLIDECOLD_BASE_URL`, or the shared config file.
- `*Error` with `IsAuthError`, `IsPermissionError`, `IsNotFound` and `IsRateLimited`; GET retries with backoff.
- `Accounts.WaitForLoginJob` and `VerifyWebhookSignature`.
- `slidecold` CLI with `login`, `logout` and a command for every operation.
- `slidecold-mcp` stdio MCP server with one tool per operation.
