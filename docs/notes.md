# Notes

## 校验请求body
因为http body是流，在校验时，会先读出来，此时不能直接转发请求，需要把读取的body重新写回请求体

## 网关错误码总结
目前感觉json返回里直接返回状态码不太对，然后有的状态码还可能有歧义，不知道ggateway的问题还是upstream的问题，后续开发后需要再修改下

### 客户端请求与拦截
- 400 Bad Request：非法 JSON 等客户端请求错误由 Gateway 拒绝，不访问上游。
- 401 Unauthorized：未授权，第 1 周尚未实现。
- 403 Forbidden：无权限，第 1 周尚未实现。
- 413 Content Too Large：JSON 请求体超过 1 MiB，由 Gateway 拒绝，不访问上游。

### 网关与上游服务异常
- 上游返回 400 或 500：说明 Upstream 已收到并处理请求，Gateway 原样传递业务状态码。
- 502 Bad Gateway：Gateway 尝试代理，但未成功连接上游，例如上游未启动。
- 504 Gateway Timeout：请求已经到达上游，但超过 Gateway 的 300 ms Deadline，Gateway 取消上游请求。
- 客户端主动取消：Gateway 不再尝试向已断开的客户端写响应，并将取消传播给 Upstream；Upstream 应监听 Context 并停止工作。

## 请求超时
gateway在给请求设定超时时，需要在原req的context上，比如
```
ctx, cancel := context.WithTimeout(r.Context(), timeout)
```
保证客户端也可以取消后续流程，还会保留RequestID,Trace等通过Context传递的数据

## Context传播
### client -> gateway
客户端没有把一个 Go context.Context 对象发给 Gateway。
Gateway 的 net/http 收到请求后，会为这个请求创建
```
客户端断开连接
    ↓ 网络事件
Gateway 的 net/http 检测到连接断开
    ↓
Gateway 中的 r.Context() 被取消
```
### gateway内部传播
```
Gateway 入站请求 r.Context()
    │
    ├── 客户端断开时取消
    │
    └── context.WithTimeout
            │
            ├── 继承父 Context 的取消
            └── 增加 Gateway 自己的超时
                    │
                    └── ReverseProxy 的出站请求 Context
```
### gateway -> upstream
```
Gateway 出站 Context 被取消
    ↓
http.Transport 停止等待或尝试中止请求
    ↓
关闭连接、取消 HTTP/2 Stream 等网络行为
    ↓
Upstream 的 net/http 可能观察到连接/流被取消
    ↓
Upstream 的 r.Context() 被取消
```
### `Handler.ServeHTTP` 是同步的

直接调用 `handler.ServeHTTP(w, r)` 时，当前 Goroutine 会同步执行整个
Handler 链，直到请求处理完成后才返回。它不会自动启动新的 Goroutine；
只有 Handler 自身显式启动异步任务时，才会产生额外的并发执行。
