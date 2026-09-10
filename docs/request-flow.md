# Gateway 请求链路

本文记录当前最小 AI Gateway 的实际 HTTP 请求生命周期。一次代理请求包含两段独立连接，Gateway 同时扮演 HTTP Server 和 HTTP Client：

```text
Downstream Client
    │  POST /v1/chat/completions (JSON or SSE)
    ▼
Gateway :8080
    │
    ├── model-a → ReverseProxy → Mock A :8091
    ├── model-b → ReverseProxy → Mock B :8092
    ├── local   → ReverseProxy → Ollama / configured real model
    └── flash / pro → ReverseProxy → DeepSeek https://api.deepseek.com

响应沿相反方向返回：Upstream → Gateway → Downstream Client。非流式响应是单个 JSON 对象；流式响应使用 Upstream 的 `text/event-stream` 和 Chat Completions chunk。
```

## 完整请求与响应时序

1. `cmd/gateway/main.go` 中的 `http.Server` 在 `:8080` 接收请求。
2. `http.ServeMux` 将 `POST /v1/chat/completions` 交给 Gateway Handler 链。
3. 最外层 `middleware.GetGatewayRequestID` 生成 Gateway 自有 ID，写入响应 Header、请求 Context 和转发请求；客户端提供的同名值不会成为 Gateway ID。可选的 `X-Client-Request-ID` 只进入 Context 和 Gateway 日志。
4. `middleware.LogRequest` 包装 `ResponseWriter`，在请求完成后记录 method、path、status、latency 和关联 ID。包装器通过 `Unwrap` 保留下层 Writer 的 Flush 等能力。
5. `middleware.ValidateJSONRequest` 读取请求体：
   - 用 `http.MaxBytesReader` 将 Body 限制为 1 MiB；
   - 拒绝非法 JSON，并使用统一 `error` envelope 返回 400；
   - 拒绝空 `messages`；
   - 将最小 Chat Completions 请求放入 Context；
   - 把校验过的原始字节放入 Context，供改写阶段重建转发体；
   - 重建 `r.Body`，让请求对象保持完整；转发不依赖这一步。
6. `middleware.RequestTimeout` 从入站 Context 派生当前统一的 30 秒 Deadline。客户端取消和 Gateway Deadline 都会继续传播。
7. `router.ModelRouter` 从 Context 读取逻辑模型：
   - 按启动时的配置映射选择对应 ReverseProxy（默认配置含 model-a、model-b、local、flash 和 pro）；
   - 缺少或未知模型时返回稳定的 400，不访问上游。
8. 选中的 ReverseProxy 从 Context 取出校验过的字节，只把顶层 `model` 替换为该 Upstream 的真实模型名，未建模字段按原始字节保留；同时把目标地址改写为配置的 Upstream，删除不应转发的客户端关联 ID 和客户端 `Authorization`，并在上游配置了凭证时写入网关自己的 `Authorization`。
9. 共享 `http.Transport` 建立或复用上游连接并发送重建后的请求。
10. `ModifyResponse` 删除 Upstream 试图设置的 Gateway ID，记录 Provider 自有的 `X-Request-ID`，随后由 ReverseProxy 复制响应状态、Header 和 Body。
11. 对 `text/event-stream`，ReverseProxy 会及时 Flush 写入；外层日志 Writer 的 `Unwrap` 使 Flush 能到达真实客户端连接。测试内可控 Upstream 已证明客户端能在流结束前收到第一块，而不是等待完整响应。
12. 代理失败时：
    - 客户端取消：记录 `client_canceled`，不再向已断开的客户端写响应；
    - Gateway 超时：返回 504 和统一错误对象；
    - 上游不可连接：返回 502 和统一错误对象。

## 组件职责

```text
ServeMux       根据 method + path 选择 Gateway Handler
Middleware     生成关联 ID、记录请求、校验 JSON、保存最小请求、发布校验字节并增加超时 Context
ModelRouter    根据逻辑 model 选择一个启动时组装的 http.Handler
ReverseProxy   改写目标与真实模型名（只改顶层 model），并转发普通响应或 SSE
Transport      管理到 Upstream 的连接、复用和取消
Upstream       返回普通 Chat Completions JSON 或兼容的 SSE chunk
```

ModelRouter 持有的是 `model → http.Handler` 映射，而不是在每次请求中创建代理。ReverseProxy 和共享 Transport 都在 Gateway 启动时创建并被并发复用。

## Body 与 Context

- 请求 Body 是一次性流。Middleware 读取并校验后把原始字节放入 Context，并重建 `r.Body` 让请求对象保持完整；**转发体来自 Context 里那份字节，不是 `r.Body`**。任何修改转发体的阶段都必须改 Context，否则改动不会生效。
- `model` Context 值只在 Gateway 进程内部用于路由，不会作为 Go 对象传给 Mock。
- ReverseProxy 使用派生后的请求 Context 创建上游请求，因此客户端取消和 Gateway Deadline 能传播到上游。
- Mock 是另一个 HTTP Server，它根据网络连接状态获得自己的请求 Context。
- 流式响应的第一块是否已经到达客户端，必须通过真实 HTTP 连接观察；请求完成后再检查 `httptest.ResponseRecorder` 的 Body 不能证明未发生缓冲。

## Header 边界

### 端到端 Header

- `Content-Type`、`User-Agent` 和普通自定义端到端 Header 会按代理规则转发；客户端没有提供 `Content-Type` 时，Gateway 补 `application/json`。
- `Authorization` 不按原样转发：Gateway 先删除客户端携带的值，只有为目标 Upstream 配置了 `api_key_env` 时才写入 `Bearer <api key>`。客户端凭证只属于客户端与 Gateway 之间。

### Hop-by-hop Header

- `Connection`、`Keep-Alive`、`Proxy-Authorization` 以及 `Connection` 声明的 Header 不跨连接传递。
- `Upgrade` 需要专门的协议升级处理，不能作为普通 Header 透传。

### Gateway 重新生成

- `X-Forwarded-For`、`X-Forwarded-Host`、`X-Forwarded-Proto` 由 Gateway 设置。
- `Host` 由 ReverseProxy 改写为选中的上游 Host。

## 当前简化

- Route、Upstream 和真实模型映射在启动时从配置生成，修改后需要重启。
- 请求改写只替换顶层 `model`；其余字段按客户端提交的 JSON 值保留（请求体会被重新序列化，键序、空白和 HTML 转义字符会变化）。
- 上游凭证通过 `api_key_env` 指定环境变量名，值不写进配置文件；缺失不会阻止 Gateway 启动，由 `/readyz` 报为不可用。
- Mock A/B 继续提供非流式确定性响应；流式工程证据由测试内 Upstream 和真实 Ollama 链路提供。
- 普通请求保留 30 秒总超时和 Server WriteTimeout；通过校验的流式请求绕过普通总超时并清除写 Deadline。取消、背压和断流已有测试，Gateway 流式指标与结束分类按[当前计划](AI_GATEWAY_PLAN.md)继续补齐。
