# Notes

## 校验请求body
因为http body是流，在校验时，会先读出来，此时不能直接转发请求，需要把读取的body重新写回请求体