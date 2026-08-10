# Gateway 请求链路

本文记录第 1 周当前实现中的实际 HTTP 请求生命周期。一次代理请求包含两段独立连接，Gateway 同时扮演 HTTP Server 和 HTTP Client：

```text
Downstream Client
    │  连接 1：请求 Gateway
    ▼
Gateway :8080
    │  连接 2：请求 Upstream
    ├── /user/*  ──► User Service  :8081
    └── /order/* ──► Order Service :8082

响应沿相反方向返回：Upstream ──► Gateway ──► Downstream Client
```

## 请求取消链路

客户端断开连接后，Gateway 的 `net/http` 会取消入站请求的
`r.Context()`。`RequestTimeout` 基于该 Context 创建的子 Context
也会随之取消，`ReverseProxy` 使用的上游请求因此被取消。

Gateway 的 `http.Transport` 随后终止上游请求。Upstream 的
`net/http` 检测到连接关闭或请求流终止后，取消它自己创建的
`r.Context()`。Upstream Handler 必须监听 `r.Context().Done()`，
否则业务计算仍可能继续执行。

### 客户端取消和 Gateway 超时

- 客户端取消：Gateway 中的错误为 `context.Canceled`；Gateway 记录 `client_canceled`，通常不会向已经断开的客户端写响应。
- Gateway 超时：中间件设置的 Deadline 到期，错误为 `context.DeadlineExceeded`；Gateway 取消上游请求，并向仍连接的客户端返回 504。

## 完整请求与响应时序

以 `POST /user/process` 为例：

1. `cmd/gateway/main.go` 中的 `http.Server` 在 `:8080` 接收下游请求。
2. `http.ServeMux` 根据 `/user/` 匹配路由，`http.StripPrefix` 将 `/user/process` 改写为 `/process`。
3. `middleware.ValidateJSONRequest` 处理 POST 请求体：
   - 用 `http.MaxBytesReader` 将请求体限制为 1 MiB；
   - 用 `io.ReadAll` 完整读取请求体并检查它是否是合法 JSON；
   - 校验失败时直接向下游返回 400 或 413，不再访问上游；
   - 校验成功后重新构造 `r.Body`，因为原请求体已经被读取完毕。
4. `proxy.BuildReverseProxy` 创建的 `httputil.ReverseProxy` 接收请求，并基于入站请求构造上游请求。
5. `Rewrite` 调用 `SetURL`，将目标地址改为对应的固定 Upstream；调用 `SetXForwarded`，补充转发来源信息。
6. 共享的 `http.Transport` 建立或复用到 Upstream 的连接，将请求发送到 `:8081` 或 `:8082`。
7. Upstream Handler 处理请求并写入状态码、响应头和响应体。
8. Gateway 收到上游响应后，`ModifyResponse` 根据最终状态码记录分类日志。
9. `httputil.ReverseProxy` 将上游响应头、状态码和响应体复制回下游连接，并负责关闭上游响应体。
10. 上游请求失败时，`ErrorHandler` 根据原因处理：
    - 客户端取消：记录 `client_canceled`，不再写错误响应；
    - Gateway 超时：返回 504；
    - 上游不可连接等代理错误：返回 502。

## Body 与 Context 的当前行为

- POST 请求体会在 JSON 校验中被 Gateway 完整读入内存，但最多读取 1 MiB；读取后必须重新放回 `r.Body` 才能继续转发。
- 非 POST 请求不会经过上述完整读取，Body 由 ReverseProxy 继续转发。
- 上游响应体没有被 Gateway 完整缓冲；ReverseProxy 收到响应头后，将响应体复制到下游，因此当前路径更接近流式转发。
- ReverseProxy 基于下游请求的 Context 构造上游请求。Gateway 为 user 和 order 上游请求添加 300 ms 超时，超时时取消出站请求并返回 504；客户端主动取消时，该取消会传播到上游，Gateway 将其记录为 `client_canceled`。

## Header 的当前边界

### End to End header 需要传递到上游的
- Content-Type
- User-Agent
- Authorization
    - 当前学习阶段暂时透传 Authorization，W4 引入鉴权后，客户端凭证由 Gateway 消费，Provider 凭证由 Gateway 单独注入。
- 普通用户自定义的,可以透传

### Hop to Hop header 不应该传递的
- Upgrade
    - 不能作为普通 End-to-end Header 直接透传；协议升级需要由代理专门处理。
- Connection以及其定义的
- Keep-Alive
- Proxy-Authorization 这是客户端向代理服务器进行验证的

### Gateway会重新生成的
- X-Forwarded-For、X-Forwarded-Host、X-Forwarded-Proto 由 Gateway 重新生成，不直接信任客户端传入值.
- Host：不原样透传，由 SetURL 重写为 Upstream Host。

### 响应 Header

- `Content-Type`、`X-Upstream-Request-ID` 等必要响应头会返回给客户端。
- `Connection` 及其声明的响应 Header 不会跨连接传递
