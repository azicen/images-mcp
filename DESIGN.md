# 架构设计

images-mcp 使用单二进制、共享生成核心和独立传输适配器。完整需求见 [已确认设计](docs/superpowers/specs/2026-10-31-forward-rest-design.md)。旧远程 MCP、SSE、upload 工具和独立上传端点已移除，不保留运行参数兼容层。

## 运行结构

```text
Agent -> standalone stdio MCP -> 生成核心 -> 中转
Agent -> forward stdio MCP -> REST server -> 生成核心 -> 中转
客户端本地文件 -----------> multipart -------> 当次参考图内存
                                              生成结果 -> S3 URL / Base64
```

- standalone：默认模式，本地 stdio MCP 及本地输出，无对象存储初始化。
- server：纯 REST，提供 GET /healthz、GET /v1/models、POST /v1/generate。
- forward：本地 stdio MCP，代理模型列表与生成，自动打开客户端文件，通过 io.Pipe/multipart.Writer 发送，不把图片序列化进 Agent 工具参数。

两个 stdio 适配器只注册 generate_image、list_models。forward 只依赖远端配置，无需加载生成配置校验或初始化中转、S3、输出目录。

## 文件职责

| 文件 | 职责 |
| --- | --- |
| main.go | CLI、显式配置加载、中转配置解析、standalone MCP、启动分流 |
| http.go | 运行模式配置、REST 鉴权、JSON/multipart 边界、HTTP 生命周期 |
| forward.go | 有界 HTTP 客户端、流式本地文件转发、forward MCP 适配 |
| relay.go | 生成请求、模型协议分发、中转响应与失败回滚 |
| images.go | 图片加载、格式/数量/总量校验、结果写入 |
| storage.go | S3 启动验证、上传、预签名、删除 |

## 配置与生命周期

CLI > 文件 > 对应环境变量 > 默认。默认 standalone、server :8080、forward 15m。配置文件路径必须绝对，不依赖 CWD 自动发现。forward URL 允许路径前缀，拒绝用户信息、查询与片段；客户端禁止跨源重定向，生成不自动重试。

standalone/server 验证模型协议、生成超时、全局输出格式。server file/both 在监听前校验 S3 桶与完整凭据；base64 不初始化 S3。standalone 输出本地绝对路径，server 输出 URL，forward 不下载或保存结果。配置字段和环境回退详见 [README.md](README.md)。

HTTP 使用有界请求体、ReadHeaderTimeout、IdleTimeout，接收进程取消后尝试优雅关闭并释放监听 watcher。forward HTTP 请求继承 MCP 调用 context 并叠加配置超时；管道写入 goroutine 在响应完成或失败时关闭 reader、等待退出，再关闭本地文件。

## REST 契约

GET /healthz 免认证。其余业务端点在配置密钥时接受 Bearer 或 X-API-Key，以 SHA-256 固定长度摘要及常量时间比较校验。服务密钥不作为中转密钥使用，无查询参数密钥支持。

REST 公共输入只含 prompt、model、size、quality、timeout、input_images。拒绝未知字段和请求级中转凭据/协议覆盖。生成结果包含 model/images，不暴露内部 route。

错误统一 JSON error：参数/MIME 400、鉴权 401、方法 405、大小 413、中转/存储 500。生成之前完成请求、模型、超时和参考图验证。

## 有界参考图处理

JSON 的 input_images 接受 URL/Data URI，不接受本地路径。multipart 接受 request JSON 或独立字段，禁止混用；独立 input_images 解析 JSON 数组。

服务端以 MultipartReader.NextPart 逐个处理，不使用 ParseMultipartForm，不写临时文件。限制单图 32 MiB、总上传图 64 MiB、最多 16 个文件、请求体 96 MiB、参数字段总计 1 MiB。检测实际 PNG/JPEG/WebP MIME，拒绝重复名字、缺失引用和未引用文件。

part:<name> 通过 prepared 映射保存二进制，生成核心按 input_images 顺序恢复，允许文件到达顺序与引用顺序不同。包含 URL/Data URI 的混合输入在核心加载时再次验证总图 64 MiB，重复引用计入总量。不会将上传参考图编码回 Base64，也不会创建独立 S3 参考图对象。

forward 保留 URL/Data URI，本地文件检查为普通文件、数量、大小和 MIME，映射为 part:file_N，逐文件流式读取并限制增长后的读取大小。无本地文件时发送 JSON。所有请求均不重试。

## 结果与失败回滚

共享生成核心负责 model/images 结果。base64/both 的 stdio 返回包含文本 JSON 和原生 MCP ImageContent。forward 读取有界远端响应（96 MiB），将远端失败转换为工具错误，Base64 成功结果恢复原生图像内容。

server file/both 仅上传 S3，结果不含本地 path。公开前缀可配，默认预签名 24h、最长 168h。多图生成任一步失败时删除本次已写入的文件/对象，删除使用独立有界 context，删除失败加入原错误。

## 验证

单元测试保留 standalone 生成与配置覆盖，REST 测试覆盖 JSON/multipart、鉴权、方法、限制、引用恢复、S3 URL 及回滚。forward 测试用 in-memory MCP、临时本地图片、模拟 REST/中转验证完整链路、模型代理、原生图像、取消、超时和重定向。

只使用本地模拟服务，不进行真实计费请求。验收命令：go test -race -cover ./...（超过 85%）、go vet ./...、go build -o bin/images-mcp .。
