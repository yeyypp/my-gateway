# My Gateway

A small Go AI Gateway that exposes a minimal Chat Completions-style endpoint and routes logical models to local mock upstreams.

## Current architecture

```text
Client
  → POST :8080/v1/chat/completions
  → JSON validation and request timeout
  → ModelRouter
      ├── model-a → ReverseProxy → Mock A :8091
      └── model-b → ReverseProxy → Mock B :8092
```

The Gateway owns routing and upstream addresses. Clients only know the Gateway URL and logical model name.

## Project layout

```text
cmd/
  gateway/          Gateway process and dependency wiring
  mock-upstream/    Parameterized Chat Completions mock
internal/
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
