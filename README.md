# AI Gateway

**中文** | [English](README.en.md)

路线图：[docs/AI_GATEWAY_PLAN.md](docs/AI_GATEWAY_PLAN.md)

一个精简的 Go AI 网关：提供最小化的 Chat Completions 接口，把逻辑模型路由到上游，并改写为上游实际使用的模型名。客户端只需知道网关地址和一个逻辑模型名。

```text
客户端 → POST :8080/v1/chat/completions → 网关（请求 ID / 校验 / 超时 / 日志）
  → ModelRouter：model-a→mock-a，model-b→mock-b，local→ollama，flash/pro→deepseek
  → 模型改写 + 反向代理 → 上游
```

## 快速开始

需要 Go 1.26.1；tmux 流程还需 `tmux`。

```sh
make dev     # 在 tmux 中启动 Gateway、Mock A、Mock B
make kill    # 停止会话
# 或分别运行：make run-mock-a / make run-mock-b / make run-gateway
```

| 进程 | 地址 | 用途 |
|---|---|---|
| Gateway | `http://localhost:8080` | 客户端接口、存活、就绪 |
| Mock A | `http://localhost:8091` | `model-a` 上游 |
| Mock B | `http://localhost:8092` | `model-b` 上游 |

默认读取 `configs/default_config.yaml`，可用 `go run ./cmd/gateway start --config <file>` 指定。启动前校验完整配置：URL 非法、重名、字段缺失或引用未知上游会启动失败；上游凭证缺失不阻塞，由就绪探针报告。

## 接口

`POST /v1/chat/completions` 的最小请求：

```json
{"model":"model-a","messages":[{"role":"user","content":"test"}],"stream":false}
```

`model` 是逻辑模型；`messages` 不能为空，且只支持文本 `role`/`content`；`stream=true` 要求上游支持 OpenAI 兼容 SSE。网关只改写顶层 `model` 字段，其余字段按客户端发送的 JSON 原样转发。

成功返回最小化的 Chat Completions 对象；网关自身产生的错误统一使用如下信封：

```json
{"error":{"message":"...","type":"...","param":"...","code":"..."}}
```

无效 JSON、取值不支持或模型未知返回 400；请求体过大返回 413；上游不可达返回 502；网关或网络超时返回 504。上游若返回明确的 HTTP 响应，状态码与响应体原样透传。

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

```sh
# 参考 CLI（首行输出网关请求 ID）
go run ./cmd/study-assistant analyze test-data/test.txt --model model-a

# 等价的原生请求
curl -i http://localhost:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"model-a","messages":[{"role":"user","content":"test"}]}'
```

把模型换成 `model-b` 即路由到 Mock B；未知模型返回 400 和统一错误信封。

## 请求关联

| 头 / 字段 | 归属 | 行为 |
|---|---|---|
| `X-Client-Request-ID` | 客户端 | 可选，网关仅记录，不转发上游 |
| `X-Gateway-Request-ID` | 网关 | 生成后返回客户端、转发上游，用于关联网关与 Mock 日志，不可被替换 |
| `X-Request-ID` | 上游 | 上游独立生成并返回，网关记录但不覆盖自身 ID |

`X-Gateway-Request-ID` 出现在网关的 `upstream response received`、`gateway request completed` 日志和所选 Mock 的 `mock request completed` 日志中，可据此把一次请求从 CLI 追到上游。日志不记录完整 Authorization、文件内容和原始请求体。

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

默认 `local` 路由假设 Ollama 运行在 macOS 主机、网关运行在 OrbStack Linux VM。先在主机 `ollama run gemma4:e2b-mlx`，再：

```sh
go run ./cmd/study-assistant analyze test-data/test.txt --model local            # 非流式
go run ./cmd/study-assistant analyze test-data/test.txt --model local --stream   # SSE：请求 ID、模型、TTFT 与增量内容
```

`curl -N` 可查看原始 SSE：兼容上游返回 `Content-Type: text/event-stream`、若干最小 Chat Completions 分块，并以 `data: [DONE]` 结束。其他环境请把 Ollama `base_url` 改为网关可达地址；它没有 `health_url`，不影响网关就绪。

## 当前限制

- 只读取 `model`、文本 `messages` 和 `stream`，其他字段原样转发。
- 流式依赖所选上游返回 OpenAI 兼容 SSE。
- 改写 `model` 会重新序列化请求体：字段值不变，键顺序、空白与 HTML 转义可能变化。
- 非流式受整体超时与服务端写超时约束；流式绕过整体超时并清除写超时。取消、背压、上游中断均有专门测试；流式指标与结束原因分类仍待办，见[路线图](docs/AI_GATEWAY_PLAN.md)。
- 配置在启动时加载，改路由需重启。
- 就绪对所有参与上游采用「任一健康」策略，不保证按模型可用。

## 验证

```sh
go test -count=1 ./... && go test -race ./... && go vet ./... && go build ./... && git diff --check
```

`TestGatewaySendFirstChunkBeforeSecondChunk` 用测试内置 SSE 上游证明：网关不会等整段响应结束才转发首块。
