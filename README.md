# My Gateway

A small Go reverse-proxy gateway project with two local upstream services.

## Project layout

```text
cmd/
  gateway/         Gateway process and dependency wiring
  user-service/    Local user-service upstream
  order-service/   Local order-service upstream
internal/
  proxy/           Reverse-proxy construction and policy
scripts/           Local development helpers
```

## Run locally

Start all three processes in a tmux session:

```sh
make dev
```

Or start them in separate terminals:

```sh
make run-user-service
make run-order-service
make run-gateway
```

The gateway listens on `:8080` and exposes:

- `/user/*` → user service on `:8081`
- `/order/*` → order service on `:8082`

For example:

```sh
curl http://localhost:8080/user/info
curl http://localhost:8080/order/info
```

## Verify

```sh
make fmt
make test
make build
```
