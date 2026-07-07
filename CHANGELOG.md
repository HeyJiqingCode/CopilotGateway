# Changelog

## [Unreleased]

### Breaking Changes

- Require an API key for all requests — the proxy now refuses to start without `COPILOT2API_API_KEY` (or `-api-key`). Clients must send the key via `Authorization: Bearer`, `x-api-key`, `x-goog-api-key`, or `?key=`. This makes the proxy safe to expose beyond localhost.
- Remove AmpCode support — the `/amp/*`, `/api/provider/*`, and `/api/*` reverse-proxy routes are gone

### Removed

- Remove the Gemini-compatible `/v1beta/models` endpoints (`generateContent`, `streamGenerateContent`, `countTokens`) — `gemini-cli` is no longer supported
- Remove the OpenAI-compatible `/v1/embeddings` endpoint — use a dedicated embedding provider such as Azure AI Foundry instead
- Remove cross-format request conversion — `/v1/chat/completions`, `/v1/responses`, and `/v1/messages` now pass through to the matching upstream endpoint only, instead of smart-routing/translating between Chat Completions, Responses, and Anthropic formats. Send each model to the endpoint its API natively speaks (Codex/GPT → `/v1/responses`, Claude → `/v1/messages`)
- Remove the `anthropic-beta: context-1m` → `-1m` model-ID rewrite — Copilot's current catalog carries the full context window on the base model ID (no `-1m` variants exist), so the rewrite produced an unknown model. Send the base model ID directly

### Features

- Add unauthenticated `/health` endpoint for cloud load balancer / k8s readiness probes
- Make the spoofed Copilot client version headers configurable via `COPILOT2API_EDITOR_VERSION`, `COPILOT2API_PLUGIN_VERSION`, and `COPILOT2API_CHAT_USER_AGENT`
- Return Copilot plan and detailed quota snapshots from `/usage`, including reset dates and `premium_interactions` remaining quota

### Bug Fixes

- Fetch model metadata with VS Code's current `model-access` headers and Copilot API version `2026-06-01`, so `/v1/models` reflects the extended context limits and model picker metadata GitHub exposes for the stored Copilot token
- Strip `output_config`/`thinking` from native `/v1/messages` requests when the target model doesn't support reasoning effort, fixing a `400 ... does not support reasoning effort` error (e.g. `claude-haiku-4.5` used as a small/fast model by Claude Desktop / Claude Code)

## [0.3.1] - 2026-04-26

### Bug Fixes

- Fix Anthropic thinking signatures being emitted as a separate block instead of attached to the currently open thinking block
- Fix Docker image crash (`exec /copilot2api: no such file or directory`) caused by dynamically-linked binary in `scratch` image — add `CGO_ENABLED=0` to CI cross-compilation
- Fix Docker multi-arch build: arm64 image was shipping the amd64 binary due to `ARG TARGETARCH=amd64` default overriding buildx's automatic platform arg
- Fix CI triggering redundant runs on tag pushes — `on: push` now scoped to `main` branch only

### CI

- Add Docker smoke test — `docker run --version` gate before pushing to prevent broken images from reaching the registry

### Docs

- Refresh README quick start and examples

## [0.3.0] - 2026-04-03

### Features

- Add Gemini-compatible `/v1beta/models` endpoints for local `gemini-cli` usage, including `generateContent`, `streamGenerateContent`, and `countTokens`
- Expose the full upstream model list on the Gemini `/v1beta/models` surface instead of limiting the listing to a small allowlist
- Add smart fallback routing between `/v1/chat/completions` and `/v1/responses`, so requests can still work when a model only supports one of the two OpenAI-compatible endpoints
- Improve OpenAI request conversion compatibility across the two endpoints, including better handling for system instructions, structured output, tool choice, reasoning state, and `previous_response_id`
- Improve Claude Code native `/v1/messages` compatibility by removing unsupported passthrough fields before forwarding requests upstream
- Add AmpCode support: chat completions via `/amp/v1/*` and `/api/provider/*` route through Copilot API; management routes (`/api/*`) and login redirects reverse-proxy to `ampcode.com`

## [0.2.0]

### Performance

- Batch SSE flushes in Anthropic streaming — flush once per upstream event instead of per translated event (~3-5x fewer syscalls)
- Flush at SSE event boundaries in native `/v1/messages` passthrough instead of every line (~3x fewer syscalls)
- Defer model alias body re-encode to only the native passthrough path — Responses and Chat Completions paths skip the JSON round-trip entirely
- Remove unnecessary `string()` copy in `writeSSEEvent`

### Architecture

- Consolidate models cache — single upstream `/models` fetch populates both raw JSON (for proxying) and parsed model info (for capability detection), eliminating duplicate HTTP calls
- Remove dead `internal/cache` package after consolidation
- Centralize request body size limit as `upstream.MaxRequestBody` constant (was magic number `10<<20` in 3 files)
- Consistent SSE header setup via `sse.BeginSSE()` across all streaming paths

### Logging

- nginx-style single access log per request at completion with method, endpoint, model, route, duration
- Downgrade client disconnect / context cancellation errors from ERROR to WARN via `upstream.LogRequestError`
- Add `duration_ms` to token refresh logs
- Promote key request lifecycle logs to Info level (was all Debug — invisible in default mode)
- Remove noisy per-chunk/per-event debug logs from streaming hot path
- Add `route` field to Anthropic access log (`native`, `responses`, `chat_completions`)
- Add `endpoint` field to Anthropic access log for consistency with proxy handler
- Add models cache miss debug log

### Bug Fixes

- Fix split choices in OpenAI Chat Completions responses — merge text and tool_calls from separate choices into a single Anthropic message
- Fix `AnthropicContentBlockDelta` / `AnthropicMessageDelta` type confusion in streaming events
- Remove hardcoded "Thinking..." placeholder text in thinking blocks
- Request usage in streaming chunks (`stream_options.include_usage`) so `message_delta` gets real output token counts

### Features

- 1M context window support — automatically appends `-1m` suffix when `anthropic-beta: context-1m-...` header is detected
- Document 1M context window usage in README

## [0.1.0]

- Initial commit
