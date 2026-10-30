[English](README_EN.md) | [简体中文](README.md)

# images-mcp

通过 OpenAI 兼容中转服务统一生成、编辑图片，支持本地文件或纯内存 Base64 输出。一个 Go 二进制，通过 stdio 或 HTTP 接入 MCP 客户端；HTTP 文件输出使用 S3 URL。

> 🤖 **让 AI Agent 帮你自动安装与配置？**
> 直接将这句提示词发给你的 Agent：
> `按照 https://github.com/azicen/images-mcp/blob/main/AGENTS.md 为我安装和配置 images-mcp`

## 为什么用它

- **双路路由**：统一接入 Gemini Chat Completions 与 GPT Image Images API，按模型配置选择协议。
- **统一多图编辑**：同一个 `input_images` 参数支持本地文件、URL、Data URI 和 base64；第一张为主图，后续为参考图。
- **分层可配置超时**：请求、模型及全局超时逐层覆盖，默认 10 分钟，慢速精修可设为 15 分钟。
- **单二进制输出**：运行时无需额外依赖，自动下载或解码结果，支持文件、Base64 或两者。

## 快速开始

### 1. 安装

使用满足 [go.mod](go.mod) 要求的 Go 工具链：

```sh
go install github.com/azicen/images-mcp@latest
```

### 2. 配置

复制 [images-mcp.example.yaml](images-mcp.example.yaml) 到自己的配置目录，填写真实的中转地址、API Key 和支持的模型 ID。每个模型显式设置 `api: chat` 或 `api: images`；妥善保存包含密钥的文件。

### 3. 运行

```sh
images-mcp -config /absolute/path/to/images-mcp.yaml
```

这是 stdio 服务，由 MCP 客户端启动并通信。注册到客户端时，`command` 支持两种写法：

- **方式 A（优先推荐，简洁）**：填写 `"images-mcp"`，前提是安装目录（通常为 `~/go/bin`）已加入客户端进程的 `PATH`。Hermes、Claude Code、Codex 等从该 shell 启动的 CLI 客户端默认继承此环境。可用 `command -v images-mcp`（或 `which images-mcp`）检查。
- **方式 B（兼容备选）**：填写二进制的绝对路径，如 `/absolute/path/to/images-mcp`。适用于未将 Go bin 纳入 `PATH`，或 macOS GUI 应用（如 Claude Desktop）从 Dock/Finder 启动、未继承包含 `~/go/bin` 的 shell `PATH` 的场景。

**严禁**使用 `./bin/images-mcp` 等依赖当前工作目录（CWD）的相对路径。**`-config` 参数仍必须使用绝对路径**，因为子进程启动时的 CWD 不受控，相对路径可能导致找不到配置文件；客户端配置中不要使用 `~` 或未展开的环境变量。

保存配置后重启客户端。Claude Code、Cursor、Cline、Roo Code、Hermes 的 JSON 和 Codex 的 TOML 配置见 [Agent 安装指南](AGENTS.md#步骤-3注册-mcp-客户端)。

## 推荐模型

以下为配置示例中的五款模型；实际可用性、价格和能力以中转服务为准。

| 模型 | 协议 | 推荐用途 |
| --- | --- | --- |
| `gemini-3.1-flash-image` | `chat` | 快速创作、文字排版、多图融合与角色一致性 |
| `gpt-image-2.5-flare` | `images` | 日常生成、局部修改与主体保留 |
| `gpt-image-2.5-sunburst` | `images` | 高精终稿、复杂精修；建议超时 15 分钟 |
| `gpt-image-2.5` | `images` | 通用图文编辑、自然光影与透明背景 |
| `gpt-image-2` | `images` | 批量草稿、灵感探索与缩略图 |

## 输出配置

```yaml
output_format: file # file | base64 | both
output_dir: ""
```

| 配置 | 默认值 | 说明 |
| --- | --- | --- |
| `output_format` | `file` | 全局输出格式，大小写不敏感，启动时规范化为小写；非法值阻止启动 |
| `output_dir` | `""` | 文件输出目录；留空按 `IMAGE_OUTPUT_DIR`、系统临时目录回退 |

优先级：顶层 `output_format` > `default.output_format` > `IMAGE_OUTPUT_FORMAT` > `file`。不支持请求级或模型级设置。

| 模式 | 行为 |
| --- | --- |
| `file` | 下载或解码后落盘，返回绝对 `path`，省去 Base64 内容以节省上下文 token |
| `base64` | 下载或解码后仅保存在内存，返回 `base64`；不创建目录或临时文件，零磁盘占用 |
| `both` | 落盘并同时返回 `path` 和 `base64` |

`base64` 和 `both` 同时返回原生 MCP ImageContent、JSON 文本及结构化数据，便于图像客户端渲染和纯文本客户端读取。

## MCP 工具

| 工具 | 用途 | 最小参数 |
| --- | --- | --- |
| `list_models` | 查看已配置模型、用途及默认模型 | `{}` |
| `generate_image` | 生成或编辑图片，按全局格式输出 | `{"prompt":"水彩风格的山间风景"}` |

`generate_image` 可传 `model`、`input_images` 和 `timeout`；`size`、`quality` 仅适用于 Images 路由。结果包含 `model`、`route`，以及每张图片的 `mime_type`、`bytes`，并按全局 `output_format` 返回 `path` 或 `base64`（或两者）。

输入限 PNG/JPEG/WebP，最多 16 张，每张 32 MiB、合计 64 MiB。`list_models` 只列出配置文件中的模型条目，不查询中转服务。

## 文档导航

| 文档 | 内容 |
| --- | --- |
| [AGENTS.md](AGENTS.md) | Agent 自动安装、完整最小配置、客户端注册、连通验证与自愈 |
| [images-mcp.example.yaml](images-mcp.example.yaml) | 带注释的完整配置模板及模型指南 |
| [DESIGN.md](DESIGN.md) | 路由、Schema、凭据与超时解析及图像限制的设计说明 |

开发验证与构建：

```sh
go test -race -cover ./...
go build -o bin/images-mcp .
```

## HTTP 服务与 S3 输出

HTTP 模式使用官方 Go SDK 的 Streamable HTTP；默认仍为 stdio。在原有模型、中转配置之外增加：

```yaml
server:
  transport: http
  listen: "127.0.0.1:8080"
  api_key: "" # 通过 MCP_SERVER_KEY 注入真实服务密钥
  enable_sse: false
s3:
  endpoint: https://storage.example
  bucket: generated-images
  region: us-east-1
  access_key_id: "" # 通过 AWS_ACCESS_KEY_ID 注入
  secret_access_key: "" # 通过 AWS_SECRET_ACCESS_KEY 注入
  use_path_style: true
  key_prefix: images
  public_url_prefix: ""
  presign_ttl: 24h
```

```sh
images-mcp -config /absolute/path/to/images-mcp.yaml -http 127.0.0.1:8080
```

`-http` 覆盖 `server.transport` 和 `server.listen`；`-server-key` 覆盖服务密钥。其余字段优先读取配置，再回退环境：`MCP_TRANSPORT`、`MCP_LISTEN`、`MCP_SERVER_KEY` / `SERVER_API_KEY`。不建议在命令行暴露真实密钥，优先使用权限受限配置或环境变量。

客户端连接 `http://127.0.0.1:8080/mcp`。启用 `enable_sse` 后也可使用 `http://127.0.0.1:8080/sse`。每次请求（含 SSE 后续 POST）传递 `Authorization: Bearer <服务密钥>`；也支持 `X-API-Key` 或 `?api_key=`，优先级为 Bearer、X-API-Key、查询参数。缺失或错误密钥返回 JSON 401。未配置服务密钥时不鉴权；公开部署应配置强密钥并在反向代理使用 HTTPS。查询参数容易进入日志，优先使用请求头。`GET /healthz` 无需鉴权，返回 `{"status":"ok"}`；HTTP 请求体上限为 96 MiB。

HTTP 的 `file` / `both` 必须配置 S3 bucket 和完整静态凭据，否则启动前报错，不监听端口。桶必须已存在，凭据需要 PutObject、GetObject 和 DeleteObject 权限。支持 MinIO、RustFS、R2、AWS S3；自建服务设置 endpoint，AWS 可省略，R2 通常使用 `region: auto`。环境回退包括 `S3_ENDPOINT` / `AWS_ENDPOINT_URL_S3`、`S3_BUCKET`、`S3_REGION` / `AWS_REGION`、`AWS_ACCESS_KEY_ID`、`AWS_SECRET_ACCESS_KEY`、`AWS_SESSION_TOKEN`、`S3_USE_PATH_STYLE`、`S3_PUBLIC_URL_PREFIX`。显式的 `use_path_style: false` 优先于环境。

图片上传为 `{key_prefix}/YYYY/MM/DD/{random_hex}{ext}`，按实际图片 MIME 设置 Content-Type。结果的 `images[].url` 为公开前缀拼接的 URL，或默认有效 24h 的预签名 GET URL（`presign_ttl` 最长 168h；临时凭据过期会提前失效）。公开前缀必须已经能够访问相应对象，服务不会自动修改桶策略。HTTP 结果不包含本地 `path`；`both` 额外返回 Base64 与原生 MCP 图片。多图失败会尝试删除本次已上传对象，清理失败会附在错误中。成功对象的保留周期由桶生命周期策略管理。

`output_format: base64` 在 HTTP 模式下无需 S3，不上传、不落盘。stdio 始终保持原有本地绝对路径行为。修改配置后重启服务和相应客户端连接；`list_models` 不会验证中转或 S3 凭据，仅在授权生成任务中验证真实上传。

## Docker 部署

镜像：`ghcr.io/azicen/images-mcp`，运行底包为 Debian Bookworm Slim（`debian:bookworm-slim`），支持 `linux/amd64` 和 `linux/arm64`。版本发布后可拉取 `latest`，生产部署建议使用已发布的版本标签或摘要：

```sh
docker pull ghcr.io/azicen/images-mcp:latest
```

准备主机绝对路径 `/etc/images-mcp/config.yaml`，其中模型 ID 必须是中转实际支持的 ID，且每个模型显式声明 `api: chat` 或 `api: images`。使用环境变量注入中转凭据时，删除模板中的 `default`、`families` 和模型级占位凭据，避免遮蔽环境变量；设置 `output_format: file`。配置文件不会打进镜像，挂载前须确保 UID/GID `10001:10001` 可读取（例如文件属主为 10001、权限为 600）。

先在当前 shell 中安全配置下列真实环境变量，再运行；`-e NAME` 继承环境值，不将密钥写入命令文本：

```sh
docker run -d --name images-mcp --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  --mount type=bind,src=/etc/images-mcp/config.yaml,dst=/etc/images-mcp/config.yaml,readonly \
  -e OPENAI_BASE_URL -e OPENAI_API_KEY -e MCP_SERVER_KEY \
  -e S3_BUCKET -e S3_ENDPOINT -e S3_REGION -e S3_USE_PATH_STYLE \
  -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_SESSION_TOKEN \
  ghcr.io/azicen/images-mcp:latest \
  -config /etc/images-mcp/config.yaml -http :8080
curl --fail http://127.0.0.1:8080/healthz
```

HTTP 的 `file` / `both` 输出必须配置已存在的 S3 桶和完整凭据；自建存储设置 endpoint，R2 通常使用 region `auto`。`MCP_SERVER_KEY` 为独立的强服务密钥。客户端通过 `/mcp` 连接，并发送 `Authorization: Bearer <服务密钥>`；对外提供服务时通过 HTTPS 反向代理访问。Docker 管理员可查看容器环境，应限制宿主访问。配置值优先于环境回退，详见上面的 HTTP/S3 说明。

镜像默认参数为 `-http :8080`；镜像名后的参数会整体替换默认 CMD，因此指定配置文件时也需带上 `-http :8080`，或在配置中启用 HTTP。若改端口，须同步调整端口映射及 `--health-cmd`。`/healthz` 仅检查本地服务，不能证明中转或 S3 权限有效。配置变更后重建容器并重新连接客户端。

使用 `output_format: base64` 可省略 S3。stdio 模式使用 `docker run --rm -i --no-healthcheck`（不加 `-t`），挂载同一配置并在镜像名后仅传 `-config /etc/images-mcp/config.yaml`，配置中设置 `server.transport: stdio`；本地文件输出需额外挂载 UID 10001 可写的绝对输出目录。stdio 和非 8080 HTTP 模式不能直接沿用默认 HTTP 健康检查。

本地构建：`docker build -t images-mcp:local .`。GitHub Actions 在 main 推送和 PR 时只测试、构建；`v*` 标签发布版本、主次版本及 `latest` 标签。手动运行默认不推送，勾选 `push` 后发布当前分支标签；手动从版本标签运行时仍遵循标签发布条件。预发布版本的 SemVer 标签遵循 metadata-action 的规则，`v*` 标签仍会更新 `latest`。
