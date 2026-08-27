# My Gateway

A small Go AI Gateway that exposes a minimal Chat Completions-style endpoint, routes logical models to configured upstreams, and rewrites them to provider-specific model names.

## Current architecture

```text
Client
  → POST :8080/v1/chat/completions
  → JSON validation and request timeout
  → ModelRouter
      ├── model-a → mock-a / model-a
      ├── model-b → mock-b / model-b
      └── local   → ollama / gemma4:e2b-mlx
  → model rewrite
  → ReverseProxy
```

The Gateway loads model routes and upstream addresses from configuration before it starts listening. Clients only know the Gateway URL and logical model name; they do not need the selected upstream address or its real model name.

## Project layout

```text
cmd/
  gateway/          Gateway process and dependency wiring
  mock-upstream/    Parameterized Chat Completions mock
configs/            Gateway model and upstream configuration
internal/
  config/            Configuration loading and validation
  handlers/         JSON responses and mock API handler
  middleware/       Request validation, model extraction, and timeout
  proxy/            Reverse-proxy construction and failure handling
  router/           Logical model selection
scripts/            Local development helpers
docs/               Plans, request flow, and learning notes
```

## Run locally

Start Gateway, Mock A, and Mock B in a tmux session:

```sh
make dev
```

Or start them in separate terminals:

```sh
make run-mock-a
make run-mock-b
make run-gateway
```

`make run-gateway` uses `configs/default_config.yaml`. To select another configuration explicitly:

```sh
go run ./cmd/gateway start --config path/to/config.yaml
```

The Gateway validates the complete configuration before listening on port 8080. Invalid URLs, duplicate names, missing fields, and model references to unknown upstreams cause startup to fail.

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
  - name: mock-b
    base_url: http://localhost:8092
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

## Try the routes

Route to Mock A:

```sh
curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"model-a","messages":[{"role":"user","content":"test"}]}'
```

Route to Mock B by changing the logical model:

```sh
curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"model-b","messages":[{"role":"user","content":"test"}]}'
```

Unknown models return HTTP 400 with the common `{"error": ...}` response envelope.

## Try the local Ollama route

The default `local` route assumes Ollama is running on the macOS host and the Gateway is running inside an OrbStack Linux VM. Start or load the configured model on macOS:

```sh
ollama run gemma4:e2b-mlx
```

Start the Gateway in the VM, then send the logical model `local`:

```sh
go run ./cmd/gateway start --config configs/default_config.yaml

curl http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"local","messages":[{"role":"user","content":"Explain Go context cancellation."}]}'
```

The Gateway routes the request to Ollama and rewrites `local` to `gemma4:e2b-mlx`. On environments other than OrbStack, change the Ollama `base_url` to an address reachable from the Gateway.

The Study Assistant uses the same logical route:

```sh
go run ./cmd/study-assistant analyze path/to/input.txt --model local
```

## Current protocol limits

- Only non-streaming Chat Completions requests are supported.
- The request schema currently includes only `model`, text `messages`, and `stream`.
- The Gateway re-encodes that minimal request when rewriting `model`, so unsupported or provider-specific JSON fields are not preserved yet.
- Configuration is loaded at startup; changing a route requires restarting the Gateway.

## Verify

```sh
make fmt
make test
make build
go test -race ./...
go vet ./...
```
