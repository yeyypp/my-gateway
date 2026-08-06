# Notes

## 校验请求body
因为http body是流，在校验时，会先读出来，此时不能直接转发请求，需要把读取的body重新写回请求体

## 网关错误码总结
### 客户端请求与拦截
- 400 Bad Request：判定客户端请求错误
- 401 Unauthorized：未授权
- 403 Forbidden：无权限
- 413 Content Too Large：JSON 请求体太大

### 网关与上游服务异常
- 500 Internal Server Error：上游或者 Gateway 服务故障，需要再细化
- 502 Bad Gateway：上游服务可能挂了
- 504 Gateway Timeout：上游服务超时

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
