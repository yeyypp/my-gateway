# My Gateway

A small Go AI Gateway that exposes a minimal Chat Completions-style endpoint, routes logical models to configured upstreams, and rewrites them to provider-specific model names. The Study Assistant is a reference client: it knows only the Gateway address and a logical model name.

## Current architecture

```text
Study Assistant or HTTP client
  → POST :8080/v1/chat/completions
  → Gateway request ID, validation, timeout, and request logging
  → ModelRouter
      ├── model-a → mock-a / model-a
      ├── model-b → mock-b / model-b
      └── local   → ollama / gemma4:e2b-mlx
  → model rewrite and reverse proxy
  → upstream response and correlated logs
```

The Gateway loads model routes, upstream addresses, and optional health URLs from configuration before it starts listening. Clients do not need the selected upstream address or its real model name.

## Project layout

```text
cmd/
  gateway/           Gateway process and dependency wiring
  mock-upstream/     Parameterized Chat Completions mock
  study-assistant/   Reference CLI that reads a file and calls the Gateway
configs/             Gateway model and upstream configuration
internal/
  config/            Configuration loading and validation
  handlers/          JSON responses, readiness, and mock API handler
  middleware/        Request ID, logging, validation, and timeout
  proxy/             Model rewrite, reverse proxy, and failure handling
  router/            Logical model selection
scripts/             Local development helpers
docs/                Plans, request flow, and learning notes
```

## Run locally

The project requires Go 1.26.1 or a compatible Go toolchain. The tmux-based development environment also requires `tmux`.

Start Gateway, Mock A, and Mock B in one tmux session:

```sh
make dev
```

The session keeps the three service logs in separate panes and opens a fourth pane for commands. Stop the whole session with:

```sh
make kill
```

To start the services without tmux, use three terminals:

```sh
make run-mock-a
make run-mock-b
make run-gateway
```

The services listen on these addresses:

| Process | Address | Purpose |
|---|---|---|
| Gateway | `http://localhost:8080` | Client API, liveness, and readiness |
| Mock A | `http://localhost:8091` | `model-a` upstream and readiness |
| Mock B | `http://localhost:8092` | `model-b` upstream and readiness |

`make run-gateway` uses `configs/default_config.yaml`. To select another configuration explicitly:

```sh
go run ./cmd/gateway start --config path/to/config.yaml
```

The Gateway validates the complete configuration before listening. Invalid URLs, duplicate names, missing fields, and model references to unknown upstreams cause startup to fail.

## Minimal protocol

The Gateway currently accepts `POST /v1/chat/completions` with this minimal request shape:

```json
{
  "model": "model-a",
  "messages": [
    {"role": "user", "content": "test"}
  ],
  "stream": false
}
```

`model` is a client-facing logical model. `messages` must not be empty, and only text `role` and `content` fields are supported. Streaming is not implemented yet, so `stream: true` returns HTTP 400.

Successful responses use a minimal Chat Completions-style object. Gateway-generated errors use a stable envelope:

```json
{
  "error": {
    "message": "...",
    "type": "...",
    "param": "...",
    "code": "..."
  }
}
```

Invalid JSON, unsupported values, and unknown models return 400; an oversized request body returns 413; an unreachable upstream returns 502; and a Gateway or network timeout returns 504. If an upstream returns an explicit HTTP response, its status and body pass through the Gateway.

## Configure routing

Each route combines three identities:

- `logic_model`: the stable name accepted from clients;
- `upstream`: the configured service that receives the request;
- `real_model`: the model name understood by that service.

The default configuration includes two mocks and an Ollama service reachable from an OrbStack Linux VM:

```yaml
upstreams:
  - name: mock-a
    base_url: http://localhost:8091
    health_url: http://localhost:8091/readyz
  - name: mock-b
    base_url: http://localhost:8092
    health_url: http://localhost:8092/readyz
  - name: ollama
    base_url: http://host.orb.internal:11434

models:
  - logic_model: model-a
    upstream: mock-a
    real_model: model-a
  - logic_model: model-b
    upstream: mock-b
    real_model: model-b
  - logic_model: local
    upstream: ollama
    real_model: gemma4:e2b-mlx
```

To route the client-visible `model-a` to Mock B, change its target and restart the Gateway:

```yaml
models:
  - logic_model: model-a
    upstream: mock-b
    real_model: model-b
```

The client still requests `model-a`. Before forwarding, the Gateway rewrites the request body to use `model-b`, which is the name understood by Mock B.

## Send requests

Use the Study Assistant to send a file through the Gateway to Mock A:

```sh
go run ./cmd/study-assistant analyze test-data/test.txt --model model-a
```

The first output line contains the Gateway request ID, followed by the mock response:

```text
X-Gateway-Request-ID:<generated-id>
response from mock-a
```

The equivalent direct request is:

```sh
curl -i http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"model-a","messages":[{"role":"user","content":"test"}]}'
```

Change the logical model to `model-b` to route to Mock B. Unknown models return HTTP 400 with the common error envelope.

## Correlate a request across components

Three request IDs have different owners and must remain distinct:

| Header or log field | Owner | Behavior |
|---|---|---|
| `X-Client-Request-ID` / `client_request_id` | Client | Optional caller ID. The Gateway logs it but does not forward it to the upstream. |
| `X-Gateway-Request-ID` / `gateway_request_id` | Gateway | Generated by the Gateway, returned to the client, forwarded to the upstream, and used to join Gateway and Mock logs. A client or upstream cannot replace it. |
| `X-Request-ID` / `upstream_request_id` | Upstream provider | Generated by the Mock or provider, returned independently, and recorded by the Gateway without replacing its own ID. |

After a Study Assistant request, copy the printed `X-Gateway-Request-ID` and find it in:

- the Gateway's `upstream response received` log;
- the Gateway's `gateway request completed` log;
- the selected Mock's `mock request completed` log.

The Gateway upstream-response log and Mock completion log also contain the same `upstream_request_id`. This proves that one request can be followed from the CLI through the Gateway to the selected upstream while the Gateway and provider identities remain separate.

To observe an optional client-owned ID in Gateway logs, send a direct request with one:

```sh
curl -i http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -H 'X-Client-Request-ID: demo-client-001' \
  -d '{"model":"model-a","messages":[{"role":"user","content":"test"}]}'
```

Logs intentionally omit complete Authorization values, file contents, and raw Chat Completions request bodies.

## Health and readiness

| Endpoint | Meaning |
|---|---|
| Gateway `GET /healthz` | The Gateway process is alive. It does not call an upstream. |
| Gateway `GET /readyz` | At least one upstream with a configured `health_url` is currently reachable and ready. If no upstream has a health URL, readiness succeeds. |
| Mock `GET /readyz` | That Mock process is ready to receive requests. |

Check the Gateway endpoints with:

```sh
curl -i http://localhost:8080/healthz
curl -i http://localhost:8080/readyz
```

With the default configuration, both mocks participate in readiness and Ollama does not. The observable failure and recovery behavior is:

1. With both mocks running, `/readyz` returns 200.
2. Stop either mock; `/readyz` remains 200 because the other probed upstream is still available.
3. Stop both mocks; `/readyz` returns 503 while `/healthz` remains 200.
4. Restart either mock; `/readyz` returns 200 again without restarting the Gateway.

A failed business request, such as an unknown logical model, does not permanently change either health result. Readiness is recalculated from fresh upstream probes on every request.

## Try the local Ollama route

The default `local` route assumes Ollama is running on the macOS host and the Gateway is running inside an OrbStack Linux VM. Start or load the configured model on macOS:

```sh
ollama run gemma4:e2b-mlx
```

Then send the logical model `local`:

```sh
go run ./cmd/study-assistant analyze test-data/test.txt --model local
```

The Gateway routes the request to Ollama and rewrites `local` to `gemma4:e2b-mlx`. On environments other than OrbStack, change the Ollama `base_url` to an address reachable from the Gateway. Ollama has no `health_url` in the default configuration, so it does not affect Gateway readiness.

## Current protocol limits

- Only non-streaming Chat Completions requests are supported.
- The request schema currently includes only `model`, text `messages`, and `stream`.
- The Gateway re-encodes that minimal request when rewriting `model`, so unsupported or provider-specific JSON fields are not preserved yet.
- Configuration is loaded at startup; changing a route requires restarting the Gateway.
- Readiness uses an any-healthy policy across upstreams that have a configured health URL; it is not a per-model availability guarantee.

## Verify

```sh
go test -count=1 ./...
go test -race ./...
go vet ./...
go build ./...
git diff --check
```
