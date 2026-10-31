# images-mcp

[English](README_EN.md) | [简体中文](README.md)

单二进制图片生成与编辑工具，支持 Gemini Chat 与 GPT Image 协议。通过配置显式选择协议，统一生成、参考图校验、超时与输出存储。

## 三种运行模式

| mode | 面向 Agent 的接口 | 生成位置 | 参考图 |
| --- | --- | --- | --- |
| `standalone`（默认） | 本地 stdio MCP | 本地进程调用中转 | 直接读取本地路径、URL 或 Data URI |
| `server` | REST API | 服务端调用中转 | JSON URL/Data URI 或 multipart 文件 |
| `forward` | 本地 stdio MCP | 转发到 server | 自动读取本地文件并流式转发，Agent 无需上传 |

stdio 模式（standalone 与 forward）暴露 `generate_image` 与 `list_models` 工具；server 模式提供标准 RESTful HTTP 接口。

## 安装与启动

需要满足 [go.mod](go.mod) 的 Go 工具链。

```sh
go install github.com/azicen/images-mcp@latest
# 或在源码目录构建
go build -o bin/images-mcp .
```

将安装目录加入宿主客户端的 PATH，或使用已验证的二进制绝对路径。`-config` 必须是绝对路径；不传时仅使用环境变量和默认值，不从当前目录发现配置。`-h` 返回帮助并成功退出。

```sh
images-mcp -mode standalone -config /absolute/path/images-mcp.yaml
images-mcp -mode server -config /absolute/path/images-mcp.yaml -listen :8080
images-mcp -mode forward -forward-url https://images.example.com
```

复制 [images-mcp.example.yaml](images-mcp.example.yaml)，填写真实中转 URL、Key 和实际可用模型 ID；每个模型显式设置 `api: chat` 或 `api: images`。模板中的模型只是配置示例，不代表中转实际支持。禁止将占位凭据用于请求；推荐通过环境变量注入密钥，配置权限设为 `600`。

## 配置与优先级

运行参数优先级：CLI > 配置文件 > 环境变量 > 默认值。

| CLI | YAML | 环境变量 | 默认 |
| --- | --- | --- | --- |
| `-mode` | `mode` | `IMAGES_MCP_MODE` | standalone |
| `-listen` | `server.listen` | `MCP_LISTEN` | :8080 |
| `-server-key` | `server.api_key` | `MCP_SERVER_KEY` / `SERVER_API_KEY` | 空 |
| `-forward-url` | `forward.url` | `IMAGES_MCP_FORWARD_URL` | 必填（forward） |
| `-forward-key` | `forward.api_key` | `IMAGES_MCP_FORWARD_KEY` / `FORWARD_KEY` / `MCP_FORWARD_KEY` | 空 |
| `-forward-timeout` | `forward.timeout` | `IMAGES_MCP_FORWARD_TIMEOUT` | 15m |

forward 仅需远端配置，不初始化中转模型、凭据、输出目录或 S3。URL 支持部署路径前缀，禁止用户信息、查询参数、片段和跨源重定向。生成不自动重试；调用取消会传播至远端请求。

生成配置：

- 凭据逐项解析：请求 > `models.<id>` > `families.<family>` > 家族环境变量 > `default` > 全局环境变量。REST 和 forward 不接受请求级中转凭据或协议覆盖。
- 家族环境变量为 `GEMINI_BASE_URL` / `GEMINI_API_KEY`、`GPT_IMAGE_BASE_URL` / `GPT_IMAGE_API_KEY`；全局为 `OPENAI_BASE_URL` / `OPENAI_API_KEY`。
- 超时：请求 `timeout` > 模型 > `default.timeout` > 顶层 `timeout` > `IMAGE_TIMEOUT` > 10m。接受正数 Go duration 或纯秒数字符串。宿主更早的截止时间仍有效。
- 输出格式全局生效：顶层 `output_format` > `default.output_format` > `IMAGE_OUTPUT_FORMAT` > `file`。支持 `file`、`base64`、`both`（不区分大小写）。非法值阻止生成进程启动。
- standalone 的输出目录：`output_dir` > `IMAGE_OUTPUT_DIR` > 系统临时目录；返回绝对本地路径。

YAML 不支持环境变量插值。留空对应字段以使用环境回退；清除遮蔽真实默认凭据的 family 占位配置。`list_models` 仅验证配置与连通性，不证明中转或 S3 凭据有效。

## Agent 客户端

Codex 本地执行：

```toml
[mcp_servers.images-mcp]
command = "images-mcp"
args = ["-config", "/absolute/path/images-mcp.yaml"]
```

远端生成时在 Agent 所在机器运行 forward：

```toml
[mcp_servers.images-mcp]
command = "images-mcp"
args = ["-mode", "forward", "-forward-url", "https://images.example.com"]
```

通过宿主环境传入 `IMAGES_MCP_FORWARD_KEY`。Claude Code、Cursor、Cline、Roo Code、Hermes 使用对应 `mcpServers` 的 command/args 条目。保留已有配置，仅更新 images-mcp，修改后重启客户端。不要把 REST URL 当成远程 MCP URL。

工具调用示例（仅在用户授权生成时执行）：

```json
{"prompt":"水彩风格的山间风景","timeout":"15m"}
```

```json
{"prompt":"保留主体并修改背景","input_images":["/absolute/path/main.png","/absolute/path/style.jpg"]}
```

第一张为主图，其余为参考图；forward 自动处理本地路径。仅 Images 协议接受 `size`、`quality`。未配置默认模型时传 `model`。不要让 Agent 序列化大段 Base64。

## REST API

| 方法与路径 | 作用 | 鉴权 |
| --- | --- | --- |
| `GET /healthz` | `{"status":"ok"}` | 无 |
| `GET /v1/models` | 模型记录数组 | 有配置 Key 时需要 |
| `POST /v1/generate` | 生成结果 `model` / `images` | 有配置 Key 时需要 |

使用 `Authorization: Bearer <service-key>` 或 `X-API-Key`，服务密钥与中转密钥不同。公开部署配置强密钥并使用 HTTPS 反向代理。错误统一为 `{"error":"message"}`：参数/MIME 400、鉴权 401、大小超限 413、方法 405、中转/存储失败 500。

JSON 请求字段为 `prompt`、`model`、`size`、`quality`、`timeout`、`input_images`，拒绝未知字段。REST 普通参考图仅接受 HTTP(S) URL/Data URI，拒绝本地路径。

```sh
curl --fail-with-body https://images.example.com/v1/generate \
  -H "Authorization: Bearer $IMAGES_MCP_FORWARD_KEY" \
  -H 'Content-Type: application/json' \
  --data '{"prompt":"水彩山景"}'
```

multipart 可使用 `request` JSON 或独立字段（不可混用），独立 `input_images` 为 JSON 数组。通过 `part:<字段名>` 引用文件，按数组顺序恢复，文件传输顺序不影响参考顺序。

```sh
curl --fail-with-body https://images.example.com/v1/generate \
  -H "Authorization: Bearer $IMAGES_MCP_FORWARD_KEY" \
  -F 'request={"prompt":"修改背景","input_images":["part:file_0"]}' \
  -F 'file_0=@/absolute/path/main.png'
```

仅 PNG/JPEG/WebP；单图 32 MiB、总图 64 MiB、最多 16 张，请求体 96 MiB。multipart 参数合计最多 1 MiB，逐 part 解析，不写临时参考图；拒绝重复字段、缺失引用和未引用文件。参考图只用于本次生成，不单独上传到 S3。

## S3 与输出

server 的 `file` / `both` 必须在启动前配置 S3 桶和完整静态凭据，绝不返回服务端路径。`images[].url` 为公开/CDN 前缀或预签名 GET URL（默认 24h，最长 168h，临时凭据可能提前过期）。`both` 同时返回 Base64；`base64` 纯内存输出，不初始化 S3、不落盘。forward 不下载生成 URL、不保存生成文件；Base64 结果会恢复原生 MCP 图像内容。

S3 环境回退：`S3_BUCKET`、`S3_ENDPOINT` / `AWS_ENDPOINT_URL_S3`、`S3_REGION` / `AWS_REGION`、`AWS_ACCESS_KEY_ID`、`AWS_SECRET_ACCESS_KEY`、`AWS_SESSION_TOKEN`、`S3_USE_PATH_STYLE`、`S3_PUBLIC_URL_PREFIX`。配置字段优先。桶需预先存在，权限包括 PutObject/GetObject/DeleteObject；多图失败会清理本次已上传对象并报告删除错误。支持 MinIO/RustFS/R2/AWS，具体字段见模板。

## Docker

运行底包为 Debian Bookworm Slim，UID/GID 为 `10001:10001`。使用已验证、可由该用户读取的绝对配置路径，只读挂载，不把密钥写进镜像。生产固定版本或摘要。

```sh
docker run -d --name images-mcp --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  --mount type=bind,src=/etc/images-mcp/config.yaml,dst=/etc/images-mcp/config.yaml,readonly \
  -e OPENAI_BASE_URL -e OPENAI_API_KEY -e MCP_SERVER_KEY \
  -e S3_BUCKET -e S3_ENDPOINT -e S3_REGION -e S3_USE_PATH_STYLE \
  -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_SESSION_TOKEN \
  ghcr.io/azicen/images-mcp:latest \
  -config /etc/images-mcp/config.yaml -mode server -listen :8080
curl --fail http://127.0.0.1:8080/healthz
```

默认 CMD 为 `-mode server -listen :8080`；传自定义参数会替换整个 CMD，须保留模式与监听地址。配置改变后重建容器、重启客户端。容器 stdio 使用 `-i --no-healthcheck`，不分配 TTY。

## 开发验证

```sh
go test -race -cover ./...
go vet ./...
go build -o bin/images-mcp .
```

测试使用本地模拟中转与 S3，涵盖三种模式、REST 边界及 MCP forward 端到端链路，无真实计费请求。详见 [DESIGN.md](DESIGN.md) 与 [AGENTS.md](AGENTS.md)。
