# CopilotGateway

A lightweight Go gateway that exposes GitHub Copilot as OpenAI-compatible and Anthropic-compatible API endpoints, with an optional web dashboard.

> Originally forked from [whtsky/copilot2api](https://github.com/whtsky/copilot2api), now maintained independently.

## Features

- **OpenAI API Compatible**: `/v1/chat/completions`, `/v1/models`, `/v1/responses`
- **Anthropic API Compatible**: `/v1/messages`
- **API Key Auth**: Required key for all requests — safe to deploy beyond localhost
- **Streaming Support**: Full SSE streaming for both OpenAI and Anthropic formats
- **Direct Passthrough**: Each endpoint forwards to the matching Copilot API — send every model to the endpoint its API natively speaks
- **Auto Authentication**: GitHub Device Flow OAuth with automatic token refresh
- **Models Cache**: 5-minute cache for `/v1/models` and Anthropic model capability lookups
- **Web Dashboard** (optional): Login-protected page at `/` showing the live model catalog — enabled only when web credentials are configured

## Quick Start

```bash
docker run -it --rm \
  -p 127.0.0.1:7777:7777 \
  -e COPILOT2API_API_KEY=your-secret-key \
  -v ~/.config/copilot2api:/root/.config/copilot2api \
  ghcr.io/heyjiqingcode/copilotgateway:latest
```

An API key is **required** — set `COPILOT2API_API_KEY` or the proxy will refuse to start. The volume mount persists your GitHub credentials across container restarts. The examples publish the port on `127.0.0.1` only so the proxy stays local by default.

On first run, it prompt GitHub Device Flow authentication:

```
🔐 GitHub Authentication Required
Please visit: https://github.com/login/device
Enter code: XXXX-XXXX

Waiting for authorization...
✅ Authentication successful!
```

Server starts on `http://127.0.0.1:7777` by default.

## Security

- **API key is required.** Set `COPILOT2API_API_KEY` (comma-separated for multiple keys); the proxy refuses to start without it. Every request must present a matching key, so it is safe to expose beyond localhost.
- Clients send the key in whichever header their SDK uses — all of these are accepted: `Authorization: Bearer <key>`, `x-api-key: <key>`, `x-goog-api-key: <key>`, or `?key=<key>`.
- When deploying to the public internet, terminate **HTTPS** at a reverse proxy / load balancer in front of this service so keys are not sent in clear text.
- The `/health` endpoint is unauthenticated for load balancer probes.
- Credentials are stored in `~/.config/copilot2api/credentials.json`.

## Web Dashboard (optional)

A login-protected page at `/` that shows the live model catalog: the latest generation of each model series with expandable per-model details (limits, capabilities, pricing), plus one-click copy of the endpoint URL and gateway API keys.

**Disabled by default.** It only exists when **both** `COPILOT2API_WEB_USERNAME` and `COPILOT2API_WEB_PASSWORD` are set — otherwise all web paths return 404 and the proxy behaves as a pure API gateway, exactly as before.

- Sign in at `/login`; sessions are in-memory cookies (lifetime `COPILOT2API_WEB_SESSION_HOURS`, default 24h) and reset on restart.
- Web paths (`/`, `/login`, `/auth/*`, `/web/*`) are guarded by the cookie session instead of the API key; `/v1/*` authentication is unchanged.
- Signed-in users can view and copy the gateway API keys from the dashboard — pick a strong web password when exposing it publicly.

## Usage with Anthropic

### Claude Code

Add to `~/.claude/settings.json`:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:7777",
    "ANTHROPIC_API_KEY": "your-secret-key",
    "ANTHROPIC_MODEL": "claude-opus-4.8",
    "ANTHROPIC_SMALL_FAST_MODEL": "claude-haiku-4.5",
    "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC": "1"
  },
  "permissions": {
    "deny": [
      "WebSearch"
    ]
  }
}
```

### Anthropic Python SDK

```python
import anthropic

client = anthropic.Anthropic(
    api_key="your-secret-key",
    base_url="http://127.0.0.1:7777"
)

message = client.messages.create(
    model="claude-sonnet-4.6",
    max_tokens=1024,
    messages=[{"role": "user", "content": "Hello!"}]
)
```

### Context Window

Newer Claude models on Copilot (e.g. Opus 4.6/4.7/4.8, Sonnet 4.6/5) expose their context limits through the Copilot `/models` catalog on the base model ID. There are no `-1m` model variants to append; send the model ID as-is.

The `/v1/models` proxy uses VS Code-style model metadata headers (`model-access`) and Copilot API version `2026-06-01`, matching the model catalog shape VS Code currently uses for long-context metadata. If VS Code shows a larger context window than this proxy, check that `COPILOT2API_API_VERSION` is not overridden to an older value.

## Usage OpenAI

### Codex

Add to `~/.codex/config.toml`:

```toml
model = "gpt-5.3-codex"
model_provider = "copilot2api"
model_reasoning_effort = "high"
web_search = "disabled"

[model_providers.copilot2api]
name = "copilot2api"
base_url = "http://127.0.0.1:7777/v1"
wire_api = "responses"
api_key = "your-secret-key"
```

### OpenAI Python SDK

```python
import openai

client = openai.OpenAI(
    api_key="your-secret-key",
    base_url="http://127.0.0.1:7777/v1"
)

response = client.chat.completions.create(
    model="gpt-5.3-codex",
    messages=[{"role": "user", "content": "Hello!"}]
)
```

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/chat/completions` | POST | OpenAI Chat Completions (streaming & non-streaming) |
| `/v1/responses` | POST | OpenAI Responses API |
| `/v1/messages` | POST | Anthropic Messages API (streaming & non-streaming) |
| `/v1/models` | GET | List available models (5min cache) |
| `/health` | GET | Health check (unauthenticated) |

## Configuration

### CLI Flags

```
./copilot2api [options]

  -host string       Server host (default "127.0.0.1")
  -port int          Server port (default 7777)
  -token-dir string  Token storage directory (default ~/.config/copilot2api)
  -api-key string    Required API key(s), comma-separated (env: COPILOT2API_API_KEY)
  -debug             Enable debug logging
  -version           Show version and exit
```

### Environment Variables

Environment variables are used as defaults when flags are not provided:

| Variable | Description | Default |
|----------|-------------|---------|
| `COPILOT2API_API_KEY` | Required API key(s) for clients, comma-separated | _none — required_ |
| `COPILOT2API_HOST` | Server host | `127.0.0.1` |
| `COPILOT2API_PORT` | Server port | `7777` |
| `COPILOT2API_TOKEN_DIR` | Token storage directory | `~/.config/copilot2api` |
| `COPILOT2API_DEBUG` | Enable debug logging (`true`/`false`, `1`/`0`) | `false` |
| `COPILOT2API_EDITOR_VERSION` | Spoofed editor version header | `vscode/1.127.0` |
| `COPILOT2API_PLUGIN_VERSION` | Spoofed plugin version header | `copilot-chat/0.55.0` |
| `COPILOT2API_CHAT_USER_AGENT` | Spoofed Copilot Chat User-Agent | `GitHubCopilotChat/0.55.0` |
| `COPILOT2API_API_VERSION` | GitHub Copilot API version header | `2026-06-01` |
| `COPILOT2API_WEB_USERNAME` | Web dashboard login username (dashboard enabled only when username & password are both set) | _unset — dashboard disabled_ |
| `COPILOT2API_WEB_PASSWORD` | Web dashboard login password | _unset — dashboard disabled_ |
| `COPILOT2API_WEB_SESSION_HOURS` | Web dashboard session lifetime (hours) | `24` |

CLI flags take precedence over environment variables.

## How It Works

1. Authenticates with GitHub via Device Flow OAuth
2. Exchanges GitHub token for Copilot API token (auto-refreshes)
3. Forwards each request straight to the matching Copilot endpoint — OpenAI formats to `/chat/completions` and `/responses`; Anthropic to `/v1/messages`
4. Automatically detects API endpoint from token (Individual/Business/Enterprise)
