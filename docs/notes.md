# Notes

## 校验请求body
因为http body是流，在校验时，会先读出来，此时不能直接转发请求，需要把读取的body重新写回请求体

## 网关错误码总结
目前感觉json返回里直接返回状态码不太对，然后有的状态码还可能有歧义，不知道ggateway的问题还是upstream的问题，后续开发后需要再修改下

invalid_request_error
表示客户端提交给 Gateway 的请求不符合 Gateway 协议，例如：
- JSON 非法；
- messages 为空；
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

## 响应传递

准确地说：

- Middleware 和 ReverseProxy 接收到的是同一个客户端 `http.ResponseWriter`。
- 但 ReverseProxy 从上游获得的 `*http.Response` 是另一个对象。
- ReverseProxy 的工作之一，就是把上游 `*http.Response` 的状态、Header 和 Body 写进这个共同的 `ResponseWriter`。

调用过程可以理解为：

```text
客户端
  ↓ Request
Request ID Middleware
  ↓ 同一个 r、同一个 w
Validate Middleware
  ↓
Router
  ↓
ReverseProxy
  ↓ 新建对上游的 HTTP 请求
Upstream
  ↓ 返回独立的 *http.Response
ReverseProxy 把它复制到原来的 w
  ↓
客户端收到响应
```

你的 Middleware 实际执行的是：

```go
w.Header().Set("X-Gateway-Request-ID", requestID)
next.ServeHTTP(w, requestWithContext)
```

`Header().Set` 此时只是修改 `ResponseWriter` 内部的 Header 集合，并没有立即把响应发送给客户端。

之后，同一个 `w` 被逐层传给 Router 和 ReverseProxy。ReverseProxy 最终执行类似下面的行为：

```text
把上游 Header 复制到 w.Header()
调用 w.WriteHeader(上游状态码)
把上游 Body 写入 w
```

第一次调用 `WriteHeader` 或 `Write` 时，Header 才会正式提交给客户端。由于 Request ID Middleware 已经提前把 Gateway ID 放进了 `w.Header()`，所以提交响应时它自然会一起发送。

响应不是这样“逐层返回新对象”的：

```text
Proxy Response → Router Response → Middleware Response
```

而是大家共同操作一个面向客户端的 `ResponseWriter`。

需要注意三个边界：

1. 内层仍然可以覆盖 Header

如果 ReverseProxy 或其他 Handler 执行：

```go
w.Header().Set("X-Gateway-Request-ID", anotherID)
```

它会覆盖 Middleware 设置的值；使用 `Add` 则可能产生多个值。所以这个 Header 要有唯一写入者。

2. Middleware 不能等 `next` 返回后才设置普通响应 Header

下面这种写法通常太晚：

```go
next.ServeHTTP(w, r)
w.Header().Set(...)
```

因为 ReverseProxy 很可能已经调用了 `WriteHeader` 或写入 Body，响应 Header 已经提交。

3. `ModifyResponse` 操作的不是客户端 `ResponseWriter`

`ModifyResponse` 收到的是上游返回的 `*http.Response`。ReverseProxy 随后才会把它复制到客户端的 `w`。因此：

```text
w.Header()
```

代表 Gateway 给客户端的响应 Header；

```text
resp.Header
```

在 `ModifyResponse` 中代表上游响应 Header。

所以你现在把 `X-Gateway-Request-ID` 放在最外层 Middleware、调用 `next` 之前，是正确的：它能覆盖所有进入该 Middleware 的成功和失败路径，同时保持 Header 的唯一所有权。

## ResponseWriter包装器
不加 `Unwrap`，当前的普通非流式请求大概率仍能正常工作，所以编译器和现有测试都可能不会提醒你。

原因是 `http.ResponseWriter` 接口本身只有三个方法：

```go
type ResponseWriter interface {
    Header() Header
    Write([]byte) (int, error)
    WriteHeader(statusCode int)
}
```

你的包装器已经实现了这些方法，因此编译完全没问题。

问题会在使用额外能力时出现。

### 不加时可能发生什么

底层 Writer 可能还支持：

```go
http.Flusher
http.Hijacker
http.Pusher
```

以及 `ResponseController` 提供的 Flush、Hijack、设置 Deadline 等能力。

包装以后：

```text
底层 Writer：支持 Write + Flush
包装器：只暴露 Write
```

调用方看到的只是包装器，于是可能认为 Flush 不受支持。

例如未来实现 SSE：

```go
controller := http.NewResponseController(w)
err := controller.Flush()
```

没有 `Unwrap` 时，可能得到：

```go
errors.Is(err, http.ErrNotSupported) == true
```

结果可能表现为：

- SSE 数据不能及时刷新；
- 客户端一直等到响应结束才收到全部内容；
- Reverse Proxy 的流式转发行为异常；
- WebSocket 或连接接管失败；
- Deadline 等高级控制不可用。

而普通 JSON 响应通常不会暴露问题。

### 加上之后

```go
func (w *responseStatusWriter) Unwrap() http.ResponseWriter {
    return w.ResponseWriter
}
```

`ResponseController` 可以继续向内寻找：

```text
responseStatusWriter
    ↓ Unwrap
底层 Writer
    ↓
Flush / Hijack / Deadline
```

### 为什么编译器不会提醒

因为 Flush、Hijack 等不属于 `http.ResponseWriter` 的必需方法，而是运行时可选能力。

编译器只能确认：

```go
var _ http.ResponseWriter = (*responseStatusWriter)(nil)
```

无法知道后续业务是否需要流式刷新或连接接管。

### 实际记忆规则

以后只要包装 `http.ResponseWriter`，可以先问一句：

> 这个包装器是否可能位于 ReverseProxy、SSE、WebSocket 或其他流式 Handler 外层？

如果可能，通常应当提供：

```go
func (w *wrapper) Unwrap() http.ResponseWriter {
    return w.ResponseWriter
}
```

但还要注意：`Unwrap` 主要帮助 `http.ResponseController`。如果第三方代码直接执行：

```go
flusher, ok := w.(http.Flusher)
```

即使提供了 `Unwrap`，这个断言仍可能失败。此时需要包装器显式实现 `Flush`，或者让调用方改用 `http.ResponseController`。

因此在你当前项目里：

- W2 非流式请求：没有 `Unwrap` 可能也看不出问题。
- 后续 W3 SSE：`Unwrap` 会成为重要的兼容性保障。

## writeStream解析
核心步骤
逐行读取：将接收到的数据流按行（\n或\r\n）分割，进行逐行处理。

缓冲行数据：维护一个缓冲区，因为一个完整的SSE事件可能会被拆分成多个网络包传输。

识别字段：判断每行的开头，识别出data:、event:等字段，并提取其后的值。

检测事件边界：当遇到一个空行（即连续的两个换行符\n\n）时，意味着一个完整的事件已经结束。

触发事件：将缓冲区内所有字段组合成一个完整的事件对象，交由业务逻辑处理（如解析JSON、拼接文本）。

清空缓冲区：处理完毕后，清空缓冲区，准备接收下一个事件。

`writeStream` 的职责可以概括成一句话：

> 持续读取 Gateway 返回的 SSE 响应，把每个完整 SSE 事件中的 JSON 解析成流式 Chat Completions chunk，然后立刻输出 `choices[].delta.content`，直到收到 `[DONE]`。

函数位于 [main.go](/home/shuai/projects/my-gateway/cmd/study-assistant/main.go:189)。

## 1. 调用前发生了什么

`writeStream` 不是直接处理整个 HTTP 响应，它只在这些条件都满足后调用：

1. HTTP 状态码是 `200`。
2. 请求开启了 `stream`。
3. 响应的 `Content-Type` 是 `text/event-stream`。
4. 已经输出了逻辑模型名 `Model:local`。

调用位置在 [main.go](/home/shuai/projects/my-gateway/cmd/study-assistant/main.go:164)：

```go
if _, err := fmt.Fprintf(out, "Model:%s\n", model); err != nil {
    return fmt.Errorf("write stream model: %w", err)
}
if err := writeStream(resp.Body, out, requestStarted); err != nil {
    return err
}
```

因此，`writeStream` 不负责：

- 发 HTTP 请求
- 检查 HTTP 状态码
- 检查 `Content-Type`
- 输出 Request ID 和模型名
- 关闭 `resp.Body`

它只负责处理 SSE 正文。

---

## 2. 三个参数的含义

```go
func writeStream(
    body io.Reader,
    out io.Writer,
    requestStarted time.Time,
) error
```

### `body io.Reader`

通常传进来的是：

```go
resp.Body
```

它不是一段已经全部下载完成的字符串，而是一条仍然可能不断收到数据的网络流。

当 Ollama/Gateway 返回一个新 chunk 时，`body` 才会逐渐有新内容可读。

### `out io.Writer`

生产环境里一般是 Cobra 的标准输出，也就是终端。

函数不直接依赖 `os.Stdout`，好处是测试时可以传：

```go
var output bytes.Buffer
```

然后检查输出内容。

### `requestStarted time.Time`

它是在执行 HTTP 请求之前记录的：

```go
requestStarted := time.Now()
resp, err := client.Do(req)
```

所以：

```go
time.Since(requestStarted)
```

包括：

- 建立连接
- Gateway 路由
- Gateway 请求 Ollama
- Ollama 推理
- Gateway 返回响应头
- 等待首个有效内容 chunk

它表示客户端观察到的 TTFT，而不仅仅是模型内部推理时间。

---

## 3. 为什么使用 `bufio.Scanner`

```go
scanner := bufio.NewScanner(body)
scanner.Buffer(make([]byte, 64*1024), 1024*1024)
```

SSE 是面向“行”的文本协议，例如：

```text
data: {"choices":[{"delta":{"content":"你好"}}]}

data: {"choices":[{"delta":{"content":"世界"}}]}

data: [DONE]

```

默认情况下，`Scanner` 使用 `ScanLines`：

- 每次 `Scan()` 读取一行
- 返回内容不包含换行符
- 如果网络暂时没有下一整行，`Scan()` 会等待
- 不需要先把整个 HTTP 响应读完

因此它适合逐行处理 SSE。

这里还把单行最大长度设置成了 1 MiB：

```go
scanner.Buffer(
    make([]byte, 64*1024), // 初始缓冲区 64 KiB
    1024*1024,             // 单行最大 1 MiB
)
```

如果某一行 SSE 超过 1 MiB，`scanner.Scan()` 会停止，最终从 `scanner.Err()` 得到错误。

这个限制是“单行限制”，不是整个响应最多只能有 1 MiB。

---

## 4. 三个状态变量

```go
done := false
firstContent := true
dataLines := make([]string, 0, 1)
```

### `done`

记录是否收到：

```text
data: [DONE]
```

只有收到 `[DONE]`，流才被认为正常结束。

如果服务器直接断开连接，却没有发 `[DONE]`，最终会返回：

```text
stream ended before [DONE]
```

### `firstContent`

用于保证 TTFT 只输出一次。

初始状态：

```go
firstContent := true
```

遇到第一个非空 `delta.content` 后：

```go
firstContent = false
```

后续 chunk 只输出内容，不再输出 TTFT。

### `dataLines`

收集当前 SSE 事件里的所有 `data:` 行。

例如：

```text
data: {"choices":
data: [{"delta":{"content":"hello"}}]}

```

将被收集为：

```go
[]string{
    `{"choices":`,
    `[{"delta":{"content":"hello"}}]}`,
}
```

然后通过：

```go
strings.Join(dataLines, "\n")
```

拼成一个完整事件。

---

## 5. 为什么要定义 `processEvent` 闭包

```go
processEvent := func() error {
    // ...
}
```

SSE 的“读取一行”和“处理一个事件”是两件不同的事。

一个事件可能有多行：

```text
event: message
id: 123
data: {...}
data: {...}

```

只有读到空行时，才说明当前事件结束，可以开始处理。

所以外层循环负责收集行，`processEvent` 负责处理已经收集完整的事件：

```text
网络字节
  → Scanner 读取一行
  → 收集 data 字段
  → 遇到空行
  → processEvent
  → JSON chunk
  → delta.content
  → 终端
```

闭包可以直接访问并修改：

- `dataLines`
- `done`
- `firstContent`
- `out`
- `requestStarted`

所以不需要把这些状态逐个作为参数传进去。

---

## 6. `processEvent` 如何处理事件

### 6.1 没有 `data:` 字段就忽略

```go
if len(dataLines) == 0 {
    return nil
}
```

例如这个 SSE 事件：

```text
event: ping
id: 123

```

没有 `data:`，因此什么都不做。

### 6.2 合并多行并清空缓存

```go
data := strings.Join(dataLines, "\n")
dataLines = dataLines[:0]
```

`strings.Join` 遵循 SSE 的多行 data 语义：多个 `data:` 字段之间插入换行符。

`dataLines[:0]` 的含义是：

- 长度清零
- 保留原来的底层数组
- 下一个事件可以复用这块内存

它不是把已经生成的 `data` 清空，因为 `strings.Join` 已经生成了新的字符串。

### 6.3 空 data 事件不处理

```go
if strings.TrimSpace(data) == "" {
    return nil
}
```

例如：

```text
data:

```

它不会被当成 JSON，也不会报错。

这里只对判断使用 `TrimSpace`，模型真正输出的 `content` 不会被 trim，因此 `" |"` 前面的空格会保留下来。

### 6.4 识别结束标记

```go
if strings.TrimSpace(data) == "[DONE]" {
    done = true
    return nil
}
```

OpenAI-compatible SSE 通常用它表示所有 chunk 已经发送完毕：

```text
data: [DONE]

```

这里没有立即从整个函数返回，而是设置 `done`。外层循环随后看到 `done`，再结束读取。

### 6.5 将 JSON 解析成协议结构

```go
var chunk protocol.ChatCompletionStreamResponse
if err := json.Unmarshal([]byte(data), &chunk); err != nil {
    return fmt.Errorf("decode stream chunk: %w", err)
}
```

对应结构位于 [chat_completions.go](/home/shuai/projects/my-gateway/internal/protocol/chat_completions.go:31)：

```go
type ChatCompletionStreamResponse struct {
    ID      string
    Object  string
    Created int64
    Model   string
    Choices []ChoiceChunk
}
```

每个 choice 是：

```go
type ChoiceChunk struct {
    Index        int
    Delta        Delta
    FinishReason *string
}
```

而当前真正使用的是：

```go
type Delta struct {
    Role    string
    Content string
}
```

例如你之前看到的响应：

```text
data: {
  "choices": [{
    "index": 0,
    "delta": {
      "content": " |"
    },
    "finish_reason": null
  }]
}
```

解析以后：

```go
chunk.Choices[0].Delta.Content == " |"
```

JSON 中额外但结构体没有声明的字段会被 `json.Unmarshal` 自动忽略。

### 6.6 遍历所有 choice

```go
for _, choice := range chunk.Choices {
    content := choice.Delta.Content
    if content == "" {
        continue
    }
```

虽然通常只有一个 choice，但协议允许返回多个，因此这里没有只取 `Choices[0]`。

这些 chunk 会被跳过：

```json
{"delta":{"role":"assistant"}}
```

```json
{"delta":{},"finish_reason":"stop"}
```

因为它们的 `delta.content` 为空。

### 6.7 首个有效内容到达时输出 TTFT

```go
if firstContent {
    if _, err := fmt.Fprintf(
        out,
        "TTFT:%s\n",
        time.Since(requestStarted),
    ); err != nil {
        return fmt.Errorf("write stream TTFT: %w", err)
    }
    firstContent = false
}
```

注意，TTFT 不是收到第一个 SSE 事件的时间，而是收到第一个非空 `delta.content` 的时间。

这是有意区分的，因为第一块可能只是：

```json
{
  "delta": {
    "role": "assistant"
  }
}
```

它没有实际可见文本，不应该被算作第一个 token。

输出大致是：

```text
TTFT:1.824321s
```

### 6.8 立即输出文本

```go
if _, err := io.WriteString(out, content); err != nil {
    return fmt.Errorf("write stream content: %w", err)
}
```

这里没有把所有内容积攒到一个字符串里，因此每处理完一个 SSE 事件，就会立即调用一次 `Write`。

例如三个 chunk：

```json
{"delta":{"content":"解释"}}
{"delta":{"content":" Go"}}
{"delta":{"content":" context"}}
```

会依次执行：

```go
io.WriteString(out, "解释")
io.WriteString(out, " Go")
io.WriteString(out, " context")
```

最终终端看到：

```text
解释 Go context
```

如果 `out` 是普通的 `os.Stdout`，通常可以立即看到。如果未来传入的是 `bufio.Writer`，调用方还需要负责 `Flush()`。

---

## 7. 外层 SSE 读取循环

核心循环是：

```go
for scanner.Scan() {
    line := strings.TrimSuffix(scanner.Text(), "\r")
    // ...
}
```

### 空行：当前 SSE 事件结束

```go
if line == "" {
    if err := processEvent(); err != nil {
        return err
    }
    if done {
        break
    }
    continue
}
```

SSE 用空行分隔事件：

```text
data: 第一个事件

data: 第二个事件

```

所以空行出现时才调用 `processEvent`。

这也意味着，如果 Gateway 已经写出了 JSON，但还没有写第二个换行形成空行，客户端仍然不会处理该事件。

为了实现及时流式返回，服务端一般要发送：

```text
data: {...}\n\n
```

并执行 HTTP flush。

### 注释行：忽略

```go
if strings.HasPrefix(line, ":") {
    continue
}
```

SSE 注释常用作心跳：

```text
: keep-alive
```

它不会进入 JSON 解析。

### 拆分字段名和值

```go
field, value, found := strings.Cut(line, ":")
```

例如：

```text
data: {"foo":"bar"}
```

得到：

```go
field == "data"
value == " {"foo":"bar"}"
```

然后移除冒号后的一个可选空格：

```go
if strings.HasPrefix(value, " ") {
    value = value[1:]
}
```

最终：

```go
value == `{"foo":"bar"}`
```

这里只删除一个空格，符合 SSE 字段解析语义，不会任意 trim 数据内容。

### 忽略非 `data` 字段

```go
if field != "data" {
    continue
}
```

以下字段目前都会被忽略：

```text
event: message
id: 123
retry: 5000
```

Study Assistant 当前只关心实际数据。

### 保存 `data:` 内容

```go
dataLines = append(dataLines, value)
```

此时不会立刻解析，因为后面可能还有另一个 `data:` 行。要等空行出现后，才调用 `processEvent`。

---

### 8. 流结束后的处理

循环可能因三种情况结束：

1. 收到 `[DONE]` 后主动 `break`
2. HTTP 响应到达 EOF
3. 读取发生错误

### 先检查读取错误

```go
if err := scanner.Err(); err != nil {
    return fmt.Errorf("read stream response: %w", err)
}
```

例如：

- 网络连接中断
- context 被取消
- response body 读取失败
- SSE 单行超过 1 MiB

都会从这里返回。

### 处理 EOF 前没有空行的最后一个事件

```go
if !done {
    if err := processEvent(); err != nil {
        return err
    }
}
```

例如服务端最后发送：

```text
data: [DONE]
```

但结尾没有额外空行，`Scanner` 到 EOF 后仍然可以在这里处理缓存中的 `[DONE]`。

### 强制要求 `[DONE]`

```go
if !done {
    return errors.New("stream ended before [DONE]")
}
```

这可以区分：

正常结束：

```text
data: [DONE]
```

异常截断：

```text
data: {"choices":[...]}

<连接突然关闭>
```

否则客户端可能把只收到一半的回答误认为完整回答。

---

### 9. 一次完整执行示例

假设 Gateway 返回：

```text
: keep-alive

data: {"choices":[{"delta":{"role":"assistant"}}]}

data: {"choices":[{"delta":{"content":"你好"}}]}

data: {"choices":[{"delta":{"content":"，世界"}}]}

data: {"choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]

```

处理过程如下：

1. 注释事件被忽略。
2. `role` chunk 成功解析，但没有 content，不输出 TTFT。
3. 收到 `"你好"`：
   - 输出一次 TTFT
   - 输出 `你好`
4. 收到 `"，世界"`：
   - 不再输出 TTFT
   - 输出 `，世界`
5. `finish_reason:"stop"` 没有 content，忽略。
6. 收到 `[DONE]`，设置 `done=true`。
7. 函数正常返回 `nil`。

结合外层逻辑，最终终端大致显示：

```text
X-Gateway-Request-ID:abc123
Model:local
TTFT:1.82s
你好，世界
```

---

### 10. 这份实现当前的边界

当前只消费：

```text
choices[].delta.content
```

还没有处理：

- `delta.tool_calls`
- `delta.function_call`
- 多模态内容
- `usage`
- `finish_reason` 的展示
- SSE 的 `id`、`event`、`retry`
- 服务端重连

另外还有几个值得明确的行为：

- 流必须以 `[DONE]` 结束。
- 单个 SSE 行最多 1 MiB。
- 元数据和回答正文写进了同一个 `out`。
- `writeStream` 本身不负责超时；读取何时中断由 request context、HTTP client、Gateway 和连接状态决定。
- 终端上的视觉折行不是 SSE 换行；只有响应字节中真实存在的换行才会被 Scanner 当作新行。
- 当前仓库里的 [main_test.go](/home/shuai/projects/my-gateway/cmd/study-assistant/main_test.go:1) 只有 225 行，我没有看到针对 `writeStream`、多块 SSE、缺少 `[DONE]` 或首块及时输出的测试。因此实现逻辑已经存在，但这些行为在当前工作树中还没有自动化测试保护。

总体来说，这个实现分成了很清楚的两层：外层按 SSE 协议确定事件边界，内层把完整 `data` 事件解析成 Chat Completions chunk。最关键的设计点就是：**空行才结束一个 SSE 事件，`data:` 行本身不等于一个完整事件。**
