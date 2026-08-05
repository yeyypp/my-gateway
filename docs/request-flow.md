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
10. 如果连接上游或读取上游响应失败，`ErrorHandler` 返回统一 JSON 格式的 502 响应。

## Body 与 Context 的当前行为

- POST 请求体会在 JSON 校验中被 Gateway 完整读入内存，但最多读取 1 MiB；读取后必须重新放回 `r.Body` 才能继续转发。
- 非 POST 请求不会经过上述完整读取，Body 由 ReverseProxy 继续转发。
- 上游响应体没有被 Gateway 完整缓冲；ReverseProxy 收到响应头后，将响应体复制到下游，因此当前路径更接近流式转发。
- ReverseProxy 基于下游请求的 Context 构造上游请求。客户端取消和 Gateway 超时能否按预期传播，将在 W1D4 和 W1D5 中用测试确认，当前不视为已经验收。

## Header 的当前边界

当前 Header 的主要传播和 Hop-by-hop Header 过滤由 Go 标准库 `httputil.ReverseProxy` 完成，项目代码只显式设置了目标 URL 和 `X-Forwarded-*` 信息。`Content-Type`、`Authorization`、`Host`、`User-Agent` 以及响应头的准确策略和测试属于 W1D3，尚未完成。
