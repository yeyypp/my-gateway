# Gateway 请求链路

本文记录当前最小 AI Gateway 的实际 HTTP 请求生命周期。一次代理请求包含两段独立连接，Gateway 同时扮演 HTTP Server 和 HTTP Client：

```text
Downstream Client
    │  POST /v1/chat/completions
    ▼
Gateway :8080
    │
    ├── model-a → ReverseProxy A → Mock A :8091
    └── model-b → ReverseProxy B → Mock B :8092

响应沿相反方向返回：Mock → Gateway → Downstream Client
```

## 完整请求与响应时序

1. `cmd/gateway/main.go` 中的 `http.Server` 在 `:8080` 接收请求。
2. `http.ServeMux` 将 `POST /v1/chat/completions` 交给 Gateway Handler 链。
3. `middleware.ValidateJSONRequest` 读取请求体：
   - 用 `http.MaxBytesReader` 将 Body 限制为 1 MiB；
   - 拒绝非法 JSON，并使用统一 `error` envelope 返回 400；
   - 提取逻辑 `model`，放入请求 Context；
   - 重建 `r.Body`，保证后续 ReverseProxy 仍能读取完整请求。
4. `middleware.RequestTimeout` 从入站 Context 派生带 5 秒 Deadline 的子 Context。客户端取消和 Gateway Deadline 都会继续传播。
5. `router.ModelRouter` 从 Context 读取逻辑模型：
   - `model-a` 选择 ReverseProxy A；
   - `model-b` 选择 ReverseProxy B；
   - 缺少或未知模型时返回稳定的 400，不访问上游。
6. 选中的 ReverseProxy 调用 `Rewrite`，把目标地址改写为 Mock A 或 Mock B，并设置转发 Header。
7. 共享 `http.Transport` 建立或复用上游连接，将原始 Chat Completions 请求发送到 `:8091` 或 `:8092`。
8. Mock Handler 解码完整请求；普通请求返回一条可辨识来源的 `chat.completion`，`stream: true` 当前返回不支持错误。
9. ReverseProxy 将上游响应状态、Header 和 Body 复制回客户端；响应 Body 不被 Gateway 完整缓冲。
10. 代理失败时：
    - 客户端取消：记录 `client_canceled`，不再向已断开的客户端写响应；
    - Gateway 超时：返回 504 和统一错误对象；
    - 上游不可连接：返回 502 和统一错误对象。

## 组件职责

```text
ServeMux       根据 method + path 选择 Gateway Handler
Middleware     校验 JSON、提取 model、增加超时 Context
ModelRouter    根据逻辑 model 选择一个 http.Handler
ReverseProxy   改写目标并正确转发 HTTP 请求与响应
Transport      管理到上游的连接、复用和取消
Mock Handler   理解最小 Chat Completions 协议并生成响应
```

ModelRouter 持有的是 `model → http.Handler` 映射，而不是在每次请求中创建代理。ReverseProxy 和共享 Transport 都在 Gateway 启动时创建并被并发复用。

## Body 与 Context

- 请求 Body 是一次性流。Middleware 读取并校验后必须重建 `r.Body`，否则 ReverseProxy 只能转发 EOF。
- `model` Context 值只在 Gateway 进程内部用于路由，不会作为 Go 对象传给 Mock。
- ReverseProxy 使用派生后的请求 Context 创建上游请求，因此客户端取消和 Gateway Deadline 能传播到上游。
- Mock 是另一个 HTTP Server，它根据网络连接状态获得自己的请求 Context。

## Header 边界

### 端到端 Header

- `Content-Type`、`User-Agent` 和当前阶段的 `Authorization` 会按代理规则转发。
- 普通自定义端到端 Header 可以透传。

### Hop-by-hop Header

- `Connection`、`Keep-Alive`、`Proxy-Authorization` 以及 `Connection` 声明的 Header 不跨连接传递。
- `Upgrade` 需要专门的协议升级处理，不能作为普通 Header 透传。

### Gateway 重新生成

- `X-Forwarded-For`、`X-Forwarded-Host`、`X-Forwarded-Proto` 由 Gateway 设置。
- `Host` 由 ReverseProxy 改写为选中的上游 Host。

## 当前简化

- 路由和端口仍硬编码，W2-M2 再配置化；
- 当前仅支持非流式文本消息；
- 5 秒请求超时只适合本地 Mock，接入真实模型时需要配置化；
- Request ID、结构化链路日志和健康检查将在后续增量补齐。
