# AI Gateway

[中文](README.md) | **English**

Roadmap: [docs/AI_GATEWAY_PLAN.md](docs/AI_GATEWAY_PLAN.md)

A small Go AI Gateway: it exposes a minimal Chat Completions endpoint, routes logical models to upstreams, and rewrites them to the model name each upstream uses. Clients only need the Gateway address, a caller credential, and a logical model name.

```text
client → POST :8080/v1/chat/completions → Gateway (identity / rate limit / request ID / validation / logging)
  → ModelRouter: model-a→mock-a, model-b→mock-b, local→ollama, flash|pro→deepseek
  → model rewrite + reverse proxy → upstream
```

## Quick start

Requires Go 1.26.1; the tmux flow also needs `tmux`.

```sh
make dev     # tmux: Gateway on the left, interactive shell on the right
make kill    # stop the session
# without tmux:
export LOCAL_CLIENT_KEY_A=local-client-a
export LOCAL_CLIENT_KEY_B=local-client-b
make run-gateway
```

The default local caller keys are `local-client-a` and `local-client-b`. Override them with `make dev LOCAL_CLIENT_KEY_A=<key> LOCAL_CLIENT_KEY_B=<key>`; both are exported into the tmux session. `make dev` does not start the mocks. Run `make run-mock-a` or `make run-mock-b` only when testing `model-a` or `model-b`.

| Process | Address | Purpose |
|---|---|---|
| Gateway | `http://localhost:8080` | Client API, liveness, readiness |
| Mock A (optional) | `http://localhost:8091` | `model-a` upstream |
| Mock B (optional) | `http://localhost:8092` | `model-b` upstream |

Reads `configs/default_config.yaml` by default; override with `go run ./cmd/gateway start --config <file>`. The full config is validated before listening: invalid URLs, duplicate names, missing fields, or unknown upstream references fail startup; a missing upstream credential does not and is reported by readiness instead.

## Protocol

Minimal `POST /v1/chat/completions` request:

```json
{"model":"model-a","messages":[{"role":"user","content":"test"}],"stream":false}
```

Requests must include `Authorization: Bearer <caller key>`. `model` is the logical model; `messages` must be non-empty and supports only text `role`/`content`; `stream=true` requires an upstream that supports OpenAI-compatible SSE. The Gateway rewrites only the top-level `model` field and forwards every other field with the JSON value the client sent.

Successful JSON and SSE responses pass through unchanged, including the real upstream model and extension fields. Gateway errors and upstream HTTP 4xx/5xx responses use an OpenAI-compatible envelope:

```json
{"error":{"message":"Rate limit exceeded.","type":"rate_limit_error","param":null,"code":"rate_limit_exceeded"}}
```

Missing or invalid credentials return 401 (`authentication_error`). Rate and concurrency rejections return 429 (`rate_limit_error`), with codes `rate_limit_exceeded` and `concurrency_limit_exceeded`. Invalid requests or unknown models return 400; oversized requests return 413 (`invalid_request_error`). Internal failures return 500, exhausted upstreams 503, and upstream timeouts 504 (`server_error`). Permission rejections use 403 / `permission_error`. Unknown paths under `/v1` return 404; unsupported chat methods return 405 with `Allow: POST`. API errors carry a Gateway Request ID. Absent `param` and `code` fields are encoded as JSON `null`.

Upstream HTTP errors retain their status and valid standard error fields. Text, HTML, empty, unreadable, or oversized error bodies become generic errors without exposing raw diagnostics. Error-body inspection is limited to 64 KiB, supports gzip, and retains `Retry-After` and upstream request IDs. HTTP errors do not trigger fallback. Interrupted SSE responses do not append JSON or synthesize `[DONE]`.

The official `github.com/openai/openai-go/v3` SDK supplies client decoding types and compatibility tests. Small server output types preserve correct null and optional-field encoding.

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

These examples use the default `local` route. Ensure the configured Ollama address is reachable and run `ollama run gemma4:e2b-mlx` on the host first. If the command is not run from the right-hand pane created by `make dev`, first set `LOCAL_CLIENT_KEY_A` in the current shell to the same value used by the Gateway.

Non-streaming request:

```sh
curl -i http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $LOCAL_CLIENT_KEY_A" \
  -H 'Content-Type: application/json' \
  -d '{"model":"local","messages":[{"role":"user","content":"用一句话介绍 Go"}],"stream":false}'
```

Streaming request (`-N` disables curl output buffering):

```sh
curl -N -i http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $LOCAL_CLIENT_KEY_A" \
  -H 'Content-Type: application/json' \
  -d '{"model":"local","messages":[{"role":"user","content":"用一句话介绍 Go"}],"stream":true}'
```

To use a mock, start its process manually and switch the model to `model-a` or `model-b`. Both normal and streaming requests work. Mock SSE sends a role chunk, content chunk, finish chunk, and `data: [DONE]`, flushing each event. Chunks share an ID, creation time, and real model; each request receives a fresh response ID. Unknown token usage is omitted instead of reporting invented zeros.

`study-assistant` reads caller credentials from `GATEWAY_API_KEY`, decodes responses with official SDK types, reports stream errors, and checks for `[DONE]`:

```sh
GATEWAY_API_KEY="$LOCAL_CLIENT_KEY_A" go run ./cmd/study-assistant analyze README.md --model model-a --stream
```

## Correlation

| Header / field | Owner | Behavior |
|---|---|---|
| `X-Client-Request-ID` | Client | Optional; the Gateway logs it but does not forward it |
| `X-Gateway-Request-ID` | Gateway | Generated, returned to the client, forwarded upstream, and used to join Gateway and Mock logs; cannot be replaced |
| `X-Request-ID` | Upstream | Generated and returned independently; the Gateway records it without replacing its own ID |

`X-Gateway-Request-ID` appears in the Gateway's `upstream response received` and `gateway request completed` logs and the selected Mock's `mock request completed` log, so a request can be followed from curl to the upstream. Logs omit complete Authorization values, file contents, and raw request bodies.

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

The default `local` route assumes Ollama on the macOS host and the Gateway in an OrbStack Linux VM. The `curl -N` example above shows the raw SSE: a compatible upstream returns `Content-Type: text/event-stream`, minimal Chat Completions chunks, and a final `data: [DONE]`. On other environments, point the Ollama `base_url` at an address reachable from the Gateway; it has no `health_url` and does not affect readiness.

## Current limits

- Reads only `model`, text `messages`, and `stream`; other fields are forwarded untouched.
- Streaming relies on the selected upstream returning OpenAI-compatible SSE.
- Caller identities currently come from two local environment variables, and per-key request-rate limits apply only within one Gateway process.
- Rewriting `model` re-serializes the request body: field values stay intact while key order, whitespace, and HTML escaping can change.
- Non-streaming requests keep the overall timeout and write deadline; streaming bypasses the overall timeout and clears the write deadline. Cancellation, backpressure, and upstream interruption have dedicated tests; streaming metrics and end-reason classification remain planned work in the [roadmap](docs/AI_GATEWAY_PLAN.md).
- Configuration is loaded at startup; changing a route requires a restart.
- Readiness uses an any-healthy policy across participating upstreams; it is not a per-model availability guarantee.

## Verify

```sh
go test -count=1 ./... && go test -race ./... && go vet ./... && go build ./... && git diff --check
```

`TestGatewaySendFirstChunkBeforeSecondChunk` uses a test-local SSE upstream to prove the Gateway forwards the first chunk without waiting for the whole response.
