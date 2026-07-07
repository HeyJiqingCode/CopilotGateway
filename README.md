# copilot2api

A lightweight Go proxy that exposes GitHub Copilot as OpenAI-compatible and Anthropic-compatible API endpoints.

## Features

- **OpenAI API Compatible**: `/v1/chat/completions`, `/v1/models`, `/v1/embeddings`, `/v1/responses`
- **Embeddings Support**: Native OpenAI-compatible `/v1/embeddings` endpoint
- **Anthropic API Compatible**: `/v1/messages`
- **API Key Auth**: Required key for all requests — safe to deploy beyond localhost
- **Streaming Support**: Full SSE streaming for both OpenAI and Anthropic formats
- **Direct Passthrough**: Each endpoint forwards to the matching Copilot API — send every model to the endpoint its API natively speaks
- **Auto Authentication**: GitHub Device Flow OAuth with automatic token refresh
- **Usage Monitoring**: Built-in `/usage` endpoint for quota tracking
- **Models Cache**: 5-minute cache for `/v1/models` and Anthropic model capability lookups

## Quick Start

### Docker

```bash
docker run -it --rm \
  -p 127.0.0.1:7777:7777 \
  -e COPILOT2API_API_KEY=your-secret-key \
  -v ~/.config/copilot2api:/root/.config/copilot2api \
  ghcr.io/whtsky/copilot2api:latest
```

An API key is **required** — set `COPILOT2API_API_KEY` or the proxy will refuse to start. The volume mount persists your GitHub credentials across container restarts. The examples publish the port on `127.0.0.1` only so the proxy stays local by default.

<details>
<summary>Docker Compose</summary>

```yaml
services:
  copilot2api:
    image: ghcr.io/whtsky/copilot2api:latest
    ports:
      - "127.0.0.1:7777:7777"
    environment:
      COPILOT2API_API_KEY: your-secret-key
    volumes:
      - ${HOME}/.config/copilot2api:/root/.config/copilot2api
```

Start it with:

```bash
docker compose up
```

</details>

### Download a release binary

```bash
# Example: macOS Apple Silicon
curl -L -o copilot2api \
  https://github.com/whtsky/copilot2api/releases/latest/download/copilot2api-darwin-arm64

# Example: Linux x64
# curl -L -o copilot2api \
#   https://github.com/whtsky/copilot2api/releases/latest/download/copilot2api-linux-amd64

chmod +x copilot2api
COPILOT2API_API_KEY=your-secret-key ./copilot2api
```

Download the asset that matches your platform from [GitHub Releases](https://github.com/whtsky/copilot2api/releases/latest). Published binaries use names like `copilot2api-linux-amd64`, `copilot2api-linux-arm64`, `copilot2api-darwin-amd64`, `copilot2api-darwin-arm64`, `copilot2api-windows-amd64.exe`, and `copilot2api-windows-arm64.exe`.

On first run, both Docker and downloaded binaries prompt GitHub Device Flow authentication:

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

## Usage with Claude Code

Add to `~/.claude/settings.json`:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:7777",
    "ANTHROPIC_API_KEY": "your-secret-key",
    "ANTHROPIC_MODEL": "claude-opus-4.6",
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

### Context Window

Newer Claude models on Copilot (e.g. Opus 4.6/4.7/4.8, Sonnet 4.6/5) expose their context limits through the Copilot `/models` catalog on the base model ID. There are no `-1m` model variants to append; send the model ID as-is.

The `/v1/models` proxy uses VS Code-style model metadata headers (`model-access`) and Copilot API version `2026-06-01`, matching the model catalog shape VS Code currently uses for long-context metadata. If VS Code shows a larger context window than this proxy, check that `COPILOT2API_API_VERSION` is not overridden to an older value.

## Usage with Codex

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

<details>
<summary>Usage with curl</summary>

```bash
# OpenAI chat completion
curl http://localhost:7777/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer your-secret-key" \
  -d '{"model":"gpt-5.3-codex","messages":[{"role":"user","content":"Hello!"}]}'

# Anthropic message
curl http://localhost:7777/v1/messages \
  -H "Content-Type: application/json" \
  -H "x-api-key: your-secret-key" \
  -d '{"model":"claude-sonnet-4.6","messages":[{"role":"user","content":"Hello!"}],"max_tokens":100}'

# List models
curl http://localhost:7777/v1/models \
  -H "Authorization: Bearer your-secret-key"

# Check usage/quota
curl http://localhost:7777/usage \
  -H "Authorization: Bearer your-secret-key"
```

</details>

<details>
<summary>Usage with SDKs</summary>

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

</details>

## API Endpoints

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/v1/chat/completions` | POST | OpenAI Chat Completions (streaming & non-streaming) |
| `/v1/responses` | POST | OpenAI Responses API |
| `/v1/models` | GET | List available models (5min cache) |
| `/v1/embeddings` | POST | Generate embeddings (string or array input) |
| `/v1/messages` | POST | Anthropic Messages API (streaming & non-streaming) |
| `/usage` | GET | Copilot usage and quota info |
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

CLI flags take precedence over environment variables.

## How It Works

1. Authenticates with GitHub via Device Flow OAuth
2. Exchanges GitHub token for Copilot API token (auto-refreshes)
3. Forwards each request straight to the matching Copilot endpoint — OpenAI formats to `/chat/completions`, `/responses`, `/embeddings`; Anthropic to `/v1/messages`
4. Automatically detects API endpoint from token (Individual/Business/Enterprise)

## Development

```bash
go test ./...              # Run tests
go build -o copilot2api .  # Build
```

## License

MIT
