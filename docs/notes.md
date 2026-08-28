# Notes

## 校验请求body
因为http body是流，在校验时，会先读出来，此时不能直接转发请求，需要把读取的body重新写回请求体

## 网关错误码总结
目前感觉json返回里直接返回状态码不太对，然后有的状态码还可能有歧义，不知道ggateway的问题还是upstream的问题，后续开发后需要再修改下

invalid_request_error
表示客户端提交给 Gateway 的请求不符合 Gateway 协议，例如：
- JSON 非法；
- messages 为空；
- stream: true 暂不支持；
- 缺少模型；
- 模型不存在；
- 请求体过大。
通常对应 400 或 413，而且 Gateway 不应访问 Upstream。
gateway_error
表示请求格式可能没有问题，但 Gateway 无法完成调用，例如：
- Gateway 内部编码失败：500；
- 无法连接 Upstream：502；
- 等待 Upstream 超过 Gateway Deadline：504。
upstream_unavailable 和 upstream_timeout 虽然名字中有 upstream，目前归入 gateway_error 是合理的。因为这份错误响应是 Gateway 生成的，而且 Gateway 只知道“我无法完成上游调用”。

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

## json相关
这个话题非常经典，也是 Go 新手最容易混淆的地方。它们的核心区别可以用一句话概括：

- **Marshal / Unmarshal**：在 **内存（变量）** 和 **字节切片（[]byte）** 之间互转（**全量**操作）。
- **Encoder / Decoder**：在 **流（io.Reader / io.Writer）** 和 **字节流** 之间互转（**增量/流式**操作）。

下面我为你详细拆解，并直接关联到你的 **AI 网关 Router** 场景中。

---

### 1. 核心区别（三维对比表）

| 维度 | Marshal / Unmarshal | Encoder / Decoder |
| :--- | :--- | :--- |
| **操作对象** | `[]byte`（内存中的字节数组） | `io.Reader` / `io.Writer`（网络连接、文件、HTTP Body 流） |
| **数据加载** | **一次性**将全部数据载入内存 | **流式**处理，边读边解，边编边写 |
| **性能特点** | 小数据极快，大数据占用大量内存 | 大数据下内存占用极低，但可能多几次系统调用 |
| **灵活性** | 无法处理“流式 JSON”（如数组里一直接收对象） | 可以处理流式 JSON，支持逐条解析 |

---

### 2. 底层原理拆解

- **Marshal（编码）**：把 Go 结构体 `stu` 序列化成 JSON 格式的 `[]byte`。  
  `json.Marshal(stu) -> []byte`

- **Unmarshal（解码）**：把 JSON 格式的 `[]byte` 反序列化进 Go 结构体。  
  `json.Unmarshal([]byte, &stu)`

- **Encoder（编码）**：把 Go 结构体直接序列化并写入 `io.Writer`（比如 `http.ResponseWriter` 或文件）。  
  `json.NewEncoder(w).Encode(stu)` —— **内部自动分块写入，不需要先生成完整的 []byte**。

- **Decoder（解码）**：从 `io.Reader`（比如 `http.Request.Body`）中读取 JSON，边读边解析进 Go 结构体。  
  `json.NewDecoder(r.Body).Decode(&stu)` —— **内部维护缓冲区，不必把整个 Body 读到内存**。

### 4. 在你的 AI 网关 Router 中该怎么选？

回顾你之前的伪代码：

> 读取并保存请求 Body → 从 JSON 中只解析 model → 恢复 r.Body

这里有**两种实现路径**，选哪一种取决于你后续是否真的需要“保存完整 Body”。


### 6. 特别提醒：`Decoder` 的“流式”陷阱

当你调用 `json.NewDecoder(r.Body).Decode(&obj)` 时，**`Decoder` 会从 `r.Body` 中消费数据**。如果解析完 `model` 后你还想把完整的 `r.Body` 传给下游，**必须提前保存**（就像你计划做的那样）。

如果直接解析完就丢掉，下游就拿不到数据了。

