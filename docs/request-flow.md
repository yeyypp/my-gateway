# 请求流向

## 两条实际路由
/user/* -> localhost:8081
/order/* -> localhost:8082

## 完整请求路径
- main.go http.Server在8080启动，接受下游请求
- http.StripPrefix把/user/info改为/info
- 用httputil.ReverseProxy实现两个代理服务，抓发请求，并接收上游响应，发送回下游