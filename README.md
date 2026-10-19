# AI Gateway

**中文** | [English](README.en.md)

路线图：[docs/AI_GATEWAY_PLAN.md](docs/AI_GATEWAY_PLAN.md)

一个精简的 Go AI 网关：提供最小化的 Chat Completions 接口，把逻辑模型路由到上游，并改写为上游实际使用的模型名。客户端只需知道网关地址、调用凭证和逻辑模型名。

```text
客户端 → POST :8080/v1/chat/completions → 网关（身份 / 限流 / 请求 ID / 校验 / 日志）
  → ModelRouter：model-a→mock-a，model-b→mock-b，local→ollama，flash/pro→deepseek
  → 模型改写 + 反向代理 → 上游
```

## 快速开始

需要 Go 1.26.1；tmux 流程还需 `tmux`。

```sh
make dev     # tmux：左侧 Gateway，右侧交互式 shell
make kill    # 停止会话
# 不使用 tmux 时：
export LOCAL_CLIENT_KEY_A=local-client-a
export LOCAL_CLIENT_KEY_B=local-client-b
make run-gateway
```

本地默认调用 Key 是 `local-client-a` 和 `local-client-b`，可用 `make dev LOCAL_CLIENT_KEY_A=<key> LOCAL_CLIENT_KEY_B=<key>` 覆盖。两个 Key 都会进入 tmux 会话环境。Mock 不随 `make dev` 启动；需要验证 `model-a` 或 `model-b` 时再分别执行 `make run-mock-a` 或 `make run-mock-b`。

| 进程 | 地址 | 用途 |
|---|---|---|
| Gateway | `http://localhost:8080` | 客户端接口、存活、就绪 |
| Mock A（可选） | `http://localhost:8091` | `model-a` 上游 |
| Mock B（可选） | `http://localhost:8092` | `model-b` 上游 |

默认读取 `configs/default_config.yaml`，可用 `go run ./cmd/gateway start --config <file>` 指定。启动前校验完整配置：URL 非法、重名、字段缺失或引用未知上游会启动失败；上游凭证缺失不阻塞，由就绪探针报告。

## 接口

`POST /v1/chat/completions` 的最小请求：

```json
{"model":"model-a","messages":[{"role":"user","content":"test"}],"stream":false}
```

请求必须携带 `Authorization: Bearer <调用方 Key>`。`model` 是逻辑模型；`messages` 不能为空，且只支持文本 `role`/`content`；`stream=true` 要求上游支持 OpenAI 兼容 SSE。网关只改写顶层 `model` 字段，其余字段按客户端发送的 JSON 原样转发。

成功 JSON 与 SSE 原样透传，保留上游真实模型名和扩展字段。网关错误及上游 HTTP 4xx/5xx 统一使用 OpenAI 兼容错误信封：

```json
{"error":{"message":"Rate limit exceeded.","type":"rate_limit_error","param":null,"code":"rate_limit_exceeded"}}
```

调用凭证缺失或无效返回 401（`authentication_error`）；频率和并发拒绝均返回 429（`rate_limit_error`），错误码分别为 `rate_limit_exceeded`、`concurrency_limit_exceeded`。参数错误或模型未知返回 400，请求体过大返回 413，均为 `invalid_request_error`；内部错误返回 500，候选全部失败返回 503，上游超时返回 504，均为 `server_error`。权限拒绝使用 403 / `permission_error`。`/v1` 下未知路径返回 404，聊天端点错误方法返回 405 和 `Allow: POST`；API 错误均携带网关 Request ID。无关联参数或错误代码时，对应字段为 JSON `null`。

上游 HTTP 错误保留状态码及有效标准错误字段；文本、HTML、空响应、读取失败和超过 64 KiB 的错误体转换为通用错误，不暴露原始内容。归一化支持 gzip，保留 `Retry-After` 和上游 Request ID。收到 HTTP 错误不会触发主备切换；SSE 开始后的中断不会追加 JSON 或伪造 `[DONE]`。

使用官方 Go SDK `github.com/openai/openai-go/v3` 解析客户端响应并进行兼容验收；服务端错误与 Mock 输出使用必要的自定义类型，以准确输出 `null` 和可选字段。

## 路由配置

每条路由由三个身份组成：`logic_model`（客户端使用的名称）、`upstream`（接收请求的服务）、`real_model`（该服务认识的模型名）。默认配置含 Mock A/B、Ollama 与 DeepSeek：

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

`api_key_env` 指定存放上游凭证的环境变量名（密钥不写入配置文件）；变量未设置不阻塞启动，但该上游会被就绪探针标记为不可用。

改路由后需重启网关。例如把 `model-a` 指向 Mock B：

```yaml
models:
  - {logic_model: model-a, upstream: mock-b, real_model: model-b}
```

客户端仍请求 `model-a`，转发前被改写为 Mock B 认识的 `model-b`。

## 发送请求

以下示例使用默认 `local` 路由。先确保默认配置中的 Ollama 地址可达，并在主机运行 `ollama run gemma4:e2b-mlx`。如果不在 `make dev` 创建的右侧窗口执行，请先在当前 shell 设置与 Gateway 相同的 `LOCAL_CLIENT_KEY_A`。

普通请求：

```sh
curl -i http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $LOCAL_CLIENT_KEY_A" \
  -H 'Content-Type: application/json' \
  -d '{"model":"local","messages":[{"role":"user","content":"用一句话介绍 Go"}],"stream":false}'
```

流式请求（`-N` 关闭 curl 输出缓冲）：

```sh
curl -N -i http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer $LOCAL_CLIENT_KEY_A" \
  -H 'Content-Type: application/json' \
  -d '{"model":"local","messages":[{"role":"user","content":"用一句话介绍 Go"}],"stream":true}'
```

要使用 Mock，可先手动启动对应进程，再把模型换成 `model-a` 或 `model-b`；普通和流式请求均可用。Mock SSE 依次输出角色块、内容块、结束块和 `data: [DONE]`，每块及时 flush，同一流的 ID、创建时间及真实模型一致。每次请求生成新的响应 ID；Mock 省略未知的 token 用量，不伪造零值。

`study-assistant` 从 `GATEWAY_API_KEY` 读取调用凭证，使用官方类型解析响应，识别流中错误并检查 `[DONE]`：

```sh
GATEWAY_API_KEY="$LOCAL_CLIENT_KEY_A" go run ./cmd/study-assistant analyze README.md --model model-a --stream
```

## 请求关联

| 头 / 字段 | 归属 | 行为 |
|---|---|---|
| `X-Client-Request-ID` | 客户端 | 可选，网关仅记录，不转发上游 |
| `X-Gateway-Request-ID` | 网关 | 生成后返回客户端、转发上游，用于关联网关与 Mock 日志，不可被替换 |
| `X-Request-ID` | 上游 | 上游独立生成并返回，网关记录但不覆盖自身 ID |

`X-Gateway-Request-ID` 出现在网关的 `upstream response received`、`gateway request completed` 日志和所选 Mock 的 `mock request completed` 日志中，可据此把一次请求从 curl 追到上游。日志不记录完整 Authorization、文件内容和原始请求体。

## 健康与就绪

| 端点 | 含义 |
|---|---|
| Gateway `GET /healthz` | 进程存活，不调用上游 |
| Gateway `GET /readyz` | 至少一个参与的上游可用：有 `health_url` 的被探测，声明了 `api_key_env` 的按凭证状态计入 |
| Mock `GET /readyz` | 该 Mock 可接收请求 |

```sh
curl -i http://localhost:8080/healthz
curl -i http://localhost:8080/readyz
```

就绪每次基于实时探测重新计算，失败的业务请求不会永久改变它。

## Ollama 与流式

默认 `local` 路由假设 Ollama 运行在 macOS 主机、网关运行在 OrbStack Linux VM。上面的 `curl -N` 可查看原始 SSE：兼容上游返回 `Content-Type: text/event-stream`、若干最小 Chat Completions 分块，并以 `data: [DONE]` 结束。其他环境请把 Ollama `base_url` 改为网关可达地址；它没有 `health_url`，不影响网关就绪。

## 当前限制

- 只读取 `model`、文本 `messages` 和 `stream`，其他字段原样转发。
- 流式依赖所选上游返回 OpenAI 兼容 SSE。
- 调用方身份目前来自两个本地环境变量，按 Key 的频率限制只在单个 Gateway 进程内生效。
- 改写 `model` 会重新序列化请求体：字段值不变，键顺序、空白与 HTML 转义可能变化。
- 非流式受整体超时与服务端写超时约束；流式绕过整体超时并清除写超时。取消、背压、上游中断均有专门测试；流式指标与结束原因分类仍待办，见[路线图](docs/AI_GATEWAY_PLAN.md)。
- 配置在启动时加载，改路由需重启。
- 就绪对所有参与上游采用「任一健康」策略，不保证按模型可用。

## 验证

```sh
go test -count=1 ./... && go test -race ./... && go vet ./... && go build ./... && git diff --check
```

`TestGatewaySendFirstChunkBeforeSecondChunk` 用测试内置 SSE 上游证明：网关不会等整段响应结束才转发首块。
