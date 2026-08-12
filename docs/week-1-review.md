# Week 1 Review
## 做了什么
第一周实现了简易的gateway，可以根据用户请求url，转发到不同的后端服务，并返回响应。
提供了不同的中间件，可以对请求进行json验证，或者添加超时。（此超时包括请求在gateway中以及在upstream中处理的时间）

## 一次请求经过哪些组件
ServeMux / StripPrefix
→ JSON 校验
→ 超时中间件
→ ReverseProxy Rewrite
→ Transport
→ Upstream

## 错误码各代表什么
- 400 代表bad request，可能是客户端请求不合法，也可能是上游返回业务400
- 500 代表 internal error， 可能是gateway有问题，或者upstream传下来的，就表示upstream有问题
- 502 代表没有正常获得上游响应
- 504 代ggateway等待自己设定的超时，然后取消上游请求，生成504
- 客户端取消，表示客户端发出请求后，可能网络断开之类的

## 生命周期
- 入站请求context，由net/http提供，客户端断开时会被取消，gateway中也可以根据入站context创建子context，reverse proxy的上游请求用这个context
- body，请求body是流，读取后，验证完，需要重新写入r.body,响应body目前不会完整读入内存，reverse proxy 会边读取上游响应，边复制给客户端

## 当前简化，后续需要改进的部分
- 后端服务不是类似大模型api的服务
- 配置硬编码
- 日志过于简单
- 各种功能没有
