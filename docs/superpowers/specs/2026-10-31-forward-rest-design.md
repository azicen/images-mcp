# stdio Forwarder 与 REST 服务架构设计

## 目标与验收

按用户指定的三种模式重构单二进制：默认 standalone 保持本地 stdio MCP；server 提供普通 REST API；forward 在客户端提供 stdio MCP，并透明读取本地参考图后发送 multipart。外部结果继续隐藏内部 route。

验收包括 `go test -race -cover ./...` 全部通过且覆盖率超过 85%、`go vet ./...`、构建 `bin/images-mcp`。验证仅使用本地模拟中转和 S3，不发起真实计费生成。

## 结构选择

采用共享生成核心、两个传输适配器的方案：保留现有生成、模型解析、图片校验与 S3 存储；REST handler 和 stdio MCP 分别负责边界；forward 使用独立 HTTP 客户端，复用 MCP 工具结果格式。

不将 REST 包装成内部 MCP 调用，因为会引入不需要的协议依赖。不另建 server/forward 二进制，因为用户要求单二进制三模式。

## 配置与启动

顶层 `mode` 支持 standalone/server/forward，默认 standalone。新增 forward.url/api_key/timeout；server 仅保留 listen/api_key。命令行提供用户指定的六类选项，优先级为命令行、文件、对应环境变量、默认值；forward 密钥按 IMAGES_MCP_FORWARD_KEY、FORWARD_KEY、MCP_FORWARD_KEY 顺序回退；server 使用既定环境变量。forward 默认超时 15m；server 默认监听 :8080。

forward 启动不要求中转模型、凭据或 S3，不初始化生成存储。校验远端 URL 为有效 HTTP(S) 地址，并禁止携带用户信息、查询参数或片段；支持部署路径前缀。禁止跨源重定向泄漏访问密钥，不自动重试生成。

server 在 file/both 输出下启动前强制检查 S3；base64 保持纯内存。standalone 继续输出本地绝对路径或 base64。显式配置文件使用绝对路径，不依赖宿主工作目录发现配置。

## REST 边界

- GET /healthz：免认证返回 {"status":"ok"}。
- GET /v1/models：返回模型记录数组，不包含凭据。
- POST /v1/generate：返回既有 model/images JSON，其中 server 输出不得包含本地 path。

模型和生成端点接受 Bearer 或 X-API-Key；配置密钥时使用常量时间校验。无配置密钥时沿用可选认证语义。错误统一返回 {"error":"message"}；参数或 MIME 错误 400、认证失败 401、大小超限 413、中转或存储失败 500；方法错误 405。

JSON 与 multipart 使用同一请求契约：prompt、model、size、quality、timeout、input_images。REST 不接受中转凭据或 API 路由覆盖。multipart 接受 request JSON 或独立字段；独立 input_images 字段使用 JSON 数组。拒绝混用两种参数表示造成的歧义。

multipart 使用有界解析，不写临时参考图；文件字段名由 part:<name> 引用，按 input_images 原顺序还原。校验重复字段、缺失引用、未引用文件、文件数量及 MIME。沿用单图 32 MiB、总图 64 MiB、最多 16 张、请求体 96 MiB 限制。JSON 或 multipart 中普通本地路径一律拒绝，避免读取服务器文件。上传内容仅作本次生成参考，不创建独立 S3 上传对象。

## Forward 边界

仅暴露 generate_image 与 list_models。list_models 代理 GET /v1/models；generate_image 将 URL/Data URI 保留，将本地文件按顺序映射为 part:file_N 并读取二进制。有本地文件时发 multipart，否则发 JSON。复用相同图片数量、大小与 MIME 限制，并向远端传播调用取消信号。

远端响应受大小限制，非成功响应转换为 MCP 工具错误；成功结果包含 URL 元数据。base64/both 响应同时恢复原生 MCP 图像内容。forward 不下载生成 URL、不落地生成文件、不自动重试。

## 测试与文档

覆盖 standalone、REST 与 forward 三种模式的测试。REST 覆盖探活、模型、两种请求、顺序、S3 URL、认证、格式与预算、错误映射。forward 使用 in-memory MCP transport 连接客户端，结合临时本地图片及模拟 HTTP 服务验证透明 multipart 全链路，补充 JSON、远端错误、取消与配置优先级测试。

同步中英 README、AGENTS、DESIGN、示例 YAML 和 Dockerfile。容器保持 Debian Bookworm Slim 与 UID/GID 10001；默认命令改为 -mode server -listen :8080，保留健康检查。文档明确本地 Agent 只需传本地路径，无需额外上传工具或大段 Base64。

## 实施顺序

1. 配置与启动模式分离，明确 standalone/server/forward 的依赖边界。
2. 替换 REST handler，实现有界 multipart 与统一错误分类。
3. 实现 forward HTTP 客户端与共享 stdio 工具结果包装。
4. 覆盖三种模式接口，补齐安全边界和端到端测试。
5. 同步部署与文档，执行 race/coverage、vet 和二进制构建。
