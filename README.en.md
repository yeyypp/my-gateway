# AI Gateway

[中文](README.md) | **English**

Roadmap: [docs/AI_GATEWAY_PLAN.md](docs/AI_GATEWAY_PLAN.md)

A small Go AI Gateway: it exposes a minimal Chat Completions endpoint, routes logical models to upstreams, and rewrites them to the model name each upstream uses. Clients only need the Gateway address and a logical model name.

```text
client → POST :8080/v1/chat/completions → Gateway (request ID / validation / timeout / logging)
  → ModelRouter: model-a→mock-a, model-b→mock-b, local→ollama, flash|pro→deepseek
  → model rewrite + reverse proxy → upstream
```

## Quick start

Requires Go 1.26.1; the tmux flow also needs `tmux`.

```sh
make dev     # start Gateway, Mock A, Mock B in tmux
make kill    # stop the session
# or run separately: make run-mock-a / make run-mock-b / make run-gateway
```

| Process | Address | Purpose |
|---|---|---|
| Gateway | `http://localhost:8080` | Client API, liveness, readiness |
| Mock A | `http://localhost:8091` | `model-a` upstream |
| Mock B | `http://localhost:8092` | `model-b` upstream |

Reads `configs/default_config.yaml` by default; override with `go run ./cmd/gateway start --config <file>`. The full config is validated before listening: invalid URLs, duplicate names, missing fields, or unknown upstream references fail startup; a missing upstream credential does not and is reported by readiness instead.

## Protocol

Minimal `POST /v1/chat/completions` request:

```json
{"model":"model-a","messages":[{"role":"user","content":"test"}],"stream":false}
```

`model` is the logical model; `messages` must be non-empty and supports only text `role`/`content`; `stream=true` requires an upstream that supports OpenAI-compatible SSE. The Gateway rewrites only the top-level `model` field and forwards every other field with the JSON value the client sent.

Success returns a minimal Chat Completions object. Gateway-generated errors use a stable envelope:

```json
{"error":{"message":"...","type":"...","param":"...","code":"..."}}
```

Invalid JSON, unsupported values, or unknown models return 400; an oversized body returns 413; an unreachable upstream returns 502; a Gateway or network timeout returns 504. An explicit upstream HTTP response passes through with its status and body.

## Routing

Each route has three identities: `logic_model` (the client-facing name), `upstream` (the service that receives the request), and `real_model` (the name that service understands). The default config includes Mock A/B, Ollama, and DeepSeek:

```yaml
upstreams:
  - {name: mock-a,   base_url: http://localhost:8091, health_url: http://localhost:8091/readyz}
  - {name: mock-b,   base_url: http://localhost:8092, health_url: http://localhost:8092/readyz}
  - {name: ollama,   base_url: http://host.orb.internal:11434}
  - {name: deepseek, base_url: https://api.deepseek.com, api_key_env: DEEPSEEK_API_KEY}
models:
  - {logic_model: model-a, upstream: mock-a,  real_model: model-a}
  - {logic_model: model-b, upstream: mock-b,  real_model: model-b}
  - {logic_model: local,   upstream: ollama,  real_model: gemma4:e2b-mlx}
  - {logic_model: flash,   upstream: deepseek, real_model: deepseek-v4-flash}
  - {logic_model: pro,     upstream: deepseek, real_model: deepseek-v4-pro}
```

`api_key_env` names the environment variable holding the upstream credential (the secret is never in the config file); if unset, startup still succeeds but readiness marks that upstream unavailable.

Changing a route requires restarting the Gateway. To point `model-a` at Mock B:

```yaml
models:
  - {logic_model: model-a, upstream: mock-b, real_model: model-b}
```

The client still requests `model-a`; it is rewritten to Mock B's `model-b` before forwarding.

## Send requests

```sh
# Reference CLI (prints the Gateway request ID on the first line)
go run ./cmd/study-assistant analyze test-data/test.txt --model model-a

# Equivalent direct request
curl -i http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"model-a","messages":[{"role":"user","content":"test"}]}'
```

Switch the model to `model-b` to route to Mock B; unknown models return 400 with the common error envelope.

## Correlation

| Header / field | Owner | Behavior |
|---|---|---|
| `X-Client-Request-ID` | Client | Optional; the Gateway logs it but does not forward it |
| `X-Gateway-Request-ID` | Gateway | Generated, returned to the client, forwarded upstream, and used to join Gateway and Mock logs; cannot be replaced |
| `X-Request-ID` | Upstream | Generated and returned independently; the Gateway records it without replacing its own ID |

`X-Gateway-Request-ID` appears in the Gateway's `upstream response received` and `gateway request completed` logs and the selected Mock's `mock request completed` log, so a request can be followed from the CLI to the upstream. Logs omit complete Authorization values, file contents, and raw request bodies.

## Health and readiness

| Endpoint | Meaning |
|---|---|
| Gateway `GET /healthz` | Process is alive; does not call an upstream |
| Gateway `GET /readyz` | At least one participating upstream is usable: those with a `health_url` are probed, those declaring `api_key_env` count by credential state |
| Mock `GET /readyz` | That Mock can receive requests |

```sh
curl -i http://localhost:8080/healthz
curl -i http://localhost:8080/readyz
```

Readiness is recalculated from fresh probes on every request, so a failed business request never changes it permanently.

## Ollama and streaming

The default `local` route assumes Ollama on the macOS host and the Gateway in an OrbStack Linux VM. Run `ollama run gemma4:e2b-mlx` on the host, then:

```sh
go run ./cmd/study-assistant analyze test-data/test.txt --model local            # non-streaming
go run ./cmd/study-assistant analyze test-data/test.txt --model local --stream   # SSE: request ID, model, TTFT, incremental content
```

`curl -N` shows the raw SSE: a compatible upstream returns `Content-Type: text/event-stream`, minimal Chat Completions chunks, and a final `data: [DONE]`. On other environments, point the Ollama `base_url` at an address reachable from the Gateway; it has no `health_url` and does not affect readiness.

## Current limits

- Reads only `model`, text `messages`, and `stream`; other fields are forwarded untouched.
- Streaming relies on the selected upstream returning OpenAI-compatible SSE.
- Rewriting `model` re-serializes the request body: field values stay intact while key order, whitespace, and HTML escaping can change.
- Non-streaming requests keep the overall timeout and write deadline; streaming bypasses the overall timeout and clears the write deadline. Cancellation, backpressure, and upstream interruption have dedicated tests; streaming metrics and end-reason classification remain planned work in the [roadmap](docs/AI_GATEWAY_PLAN.md).
- Configuration is loaded at startup; changing a route requires a restart.
- Readiness uses an any-healthy policy across participating upstreams; it is not a per-model availability guarantee.

## Verify

```sh
go test -count=1 ./... && go test -race ./... && go vet ./... && go build ./... && git diff --check
```

`TestGatewaySendFirstChunkBeforeSecondChunk` uses a test-local SSE upstream to prove the Gateway forwards the first chunk without waiting for the whole response.
