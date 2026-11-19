# search-relay-mcp

Self-hosted web search MCP with multiple providers and automatic fallback.

One Go process exposes a single `web_search` tool over Streamable HTTP. Each
call tries the configured providers in order and stops at the first usable
result set. There is no parallel aggregation, no retry inside a provider and no
anonymous fallback.

Supported providers: Keenable, TinySearch (TinyFish), AnySearch. Default order:
`keenable,tinyfish,anysearch`.

## Configuration

Copy `.env.example` to `.env` and fill in values. The binary does not read
`.env` itself; Compose or your shell must inject the variables.

| Variable | Default | Notes |
| --- | --- | --- |
| `LISTEN_ADDR` | `127.0.0.1:18081` | Keep loopback for host runs. Compose sets `0.0.0.0:18081` inside the container and publishes only `127.0.0.1:18081`. |
| `MCP_BEARER_TOKEN` | none | Required. 32-256 characters from `[A-Za-z0-9_-]`. Generate a random value and do not reuse a provider key. |
| `SEARCH_PROVIDERS` | `keenable,tinyfish,anysearch` | Ordered subset. Empty, unknown, duplicate or unkeyed entries refuse startup. |
| `KEENABLE_API_KEY` | none | Required only when Keenable is enabled. |
| `TINYFISH_API_KEY` | none | Required only when TinyFish is enabled. |
| `ANYSEARCH_API_KEY` | none | Required only when AnySearch is enabled. |

Use free tiers only. Disable auto top-up/recharge in every provider console:
this service never calls billing endpoints and does not cap spending for you.
Quotas and reset times are whatever the provider actually returns.

To run a single provider, set `SEARCH_PROVIDERS=keenable` and provide only
`KEENABLE_API_KEY`; disabled providers do not need keys.

## Run locally

Requirements: Go 1.25 or newer.

Windows PowerShell:

```powershell
$env:MCP_BEARER_TOKEN = "<random-token>"
$env:KEENABLE_API_KEY = "<key>"
$env:TINYFISH_API_KEY = "<key>"
$env:ANYSEARCH_API_KEY = "<key>"
go run ./cmd/search-relay-mcp
```

Linux/macOS:

```bash
set -a
. ./.env
set +a
go run ./cmd/search-relay-mcp
```

Health probe (does not search and does not consume quota):

```bash
curl -i http://127.0.0.1:18081/healthz
# HTTP/1.1 200 OK
# ok
```

The binary also ships a client-side probe used by the container healthcheck:

```bash
go run ./cmd/search-relay-mcp healthcheck
```

Unauthenticated MCP requests must be rejected:

```bash
curl -i -X POST http://127.0.0.1:18081/mcp
# HTTP/1.1 401 Unauthorized
```

## MCP client

Point an HTTP MCP client at `http://127.0.0.1:18081/mcp` and send the bearer
token on every request. Client configuration syntax varies; this is the common
JSON shape:

```json
{
  "mcpServers": {
    "search-relay": {
      "type": "http",
      "url": "http://127.0.0.1:18081/mcp",
      "headers": {
        "Authorization": "Bearer <random-token>"
      }
    }
  }
}
```

The server accepts native clients only: `Host` must be loopback and no
`Origin` header may be present. Browsers and OAuth flows are not supported.

To verify the relay itself, disable the client's built-in web search, then ask
the client to call `web_search` with a non-sensitive query such as
`Go release notes`. A real search consumes provider quota; `/healthz` does not.

Responses contain `structuredContent` and an equivalent JSON text block. Search
results are untrusted external data; the tool description tells clients to use
them as material, not as instructions.

## Docker Compose

```bash
cp .env.example .env
# edit .env: token plus the keys for enabled providers
docker compose up --build -d
docker compose ps
curl -i http://127.0.0.1:18081/healthz
```

The container runs read-only as a non-root user with all capabilities dropped,
`no-new-privileges`, 128 MiB memory, 0.50 CPU and a 64-process limit. Container
logs are capped at 3 x 5 MiB. Images never receive keys as build arguments and
the build context excludes `.env` and `.git`.

Stop and remove only this project:

```bash
docker compose down
```

The healthcheck subcommand only proves the process is listening. A green
container does not prove that any provider key works or that quota remains.

## Security notes

- Never commit `.env`, real keys, tokens or captured upstream responses.
- The bearer token is required on `POST`, `GET` and `DELETE /mcp`; duplicate or
  concatenated credentials are rejected.
- Request bodies are capped at 16 KiB; upstream responses are capped at 1 MiB.
- Provider logs contain only provider name, failure class, status code, latency
  and result count. Queries, keys, headers and response bodies are not logged.