# My Gateway

A small Go AI Gateway that exposes a minimal Chat Completions-style endpoint and routes logical models to local mock upstreams.

## Current architecture

```text
Client
  → POST :8080/v1/chat/completions
  → JSON validation and request timeout
  → ModelRouter
      ├── model-a → mock-a → ReverseProxy → :8091
      └── model-b → mock-b → ReverseProxy → :8092
```

The Gateway loads logical-model routes and upstream addresses from configuration before it starts listening. Clients only know the Gateway URL and logical model name.

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

Upstreams define internal service names and addresses. Models define the logical names accepted from clients and reference one of those upstreams:

```yaml
upstreams:
  - name: mock-a
    base_url: http://localhost:8091
  - name: mock-b
    base_url: http://localhost:8092

models:
  - name: model-a
    upstream: mock-a
  - name: model-b
    upstream: mock-b
```

To route `model-a` to Mock B, change only its configured upstream and restart the Gateway:

```yaml
models:
  - name: model-a
    upstream: mock-b
```

The client still requests `model-a`; it does not learn the selected upstream name or address.

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

## Verify

```sh
make fmt
make test
make build
go test -race ./...
go vet ./...
```
