# images-mcp：Agent 安装、配置与自愈

适用 Codex、Claude Code、Hermes、Cursor、Cline、Roo Code。架构以 [DESIGN.md](DESIGN.md) 与 [已确认设计](docs/superpowers/specs/2026-10-31-forward-rest-design.md) 为准。

## 硬规则

1. `command` 使用宿主 PATH 中已验证的 `images-mcp` 或可执行二进制绝对路径，禁止依赖 CWD 的相对路径。`-config` 必须是实际绝对路径，不含 `~`、未展开变量或占位符。不传配置路径时不自动发现当前目录文件。
2. 禁止猜测、打印、提交凭据或用占位值请求真实服务。缺失时继续不依赖凭据的本地构建/验证，并提示用户安全配置中转 URL、Key、实际可用模型 ID。
3. 模型显式声明 `api: chat` 或 `api: images`。不臆测模型可用性。
4. 合并客户端配置，保留其他 MCP 条目。修改配置后重启服务与宿主客户端。
5. `list_models` 只验证配置及连通性，不验证中转/S3权限。仅在用户授权的生成任务中执行生成，不自动重试可能计费的请求，不自动换模型。
6. 本地参考图直接传绝对路径；远端部署时在 Agent 所在机器运行 forward，由程序读取并发送文件。禁止让 Agent 编写大段 Base64、禁止伪造服务端本地路径。

## 1. 获取二进制

先核对 [go.mod](go.mod) 的工具链要求：

```sh
go version
go install github.com/azicen/images-mcp@latest
INSTALL_BIN_DIR="$(go env GOBIN)"
if [ -z "$INSTALL_BIN_DIR" ]; then
  INSTALL_GOPATH="$(go env GOPATH)"
  INSTALL_BIN_DIR="${INSTALL_GOPATH%%:*}/bin"
fi
ABS_BIN="$(cd "$INSTALL_BIN_DIR" && pwd -P)/images-mcp"
test -x "$ABS_BIN"
"$ABS_BIN" -h
```

要求安装成功且 `-h` 退出 0。也可在已确认的源码目录执行 `go build -o bin/images-mcp .`，再验证实际绝对二进制路径。禁止覆盖未知已有目录或误报安装成功。

## 2. 选择运行方式并配置

- standalone（默认）：本地 stdio MCP，直接调用中转，本地 file/both 返回绝对路径。
- server：纯 REST 服务，只有 `GET /healthz`、`GET /v1/models`、`POST /v1/generate`。
- forward：本地 stdio MCP，代理 REST，自动读取本地参考图并流式发送 multipart。只需远端 URL、服务密钥和超时，不初始化中转/S3。

选择实际绝对配置目录，禁止覆盖已有文件：

```sh
umask 077
mkdir -p "$HOME/.config/images-mcp"
ABS_CONFIG_DIR="$(cd "$HOME/.config/images-mcp" && pwd -P)"
ABS_CONFIG="$ABS_CONFIG_DIR/images-mcp.yaml"
if [ ! -e "$ABS_CONFIG" ]; then
  curl --fail --location https://raw.githubusercontent.com/azicen/images-mcp/main/images-mcp.example.yaml -o "$ABS_CONFIG"
fi
chmod 600 "$ABS_CONFIG"
```

源码安装可从仓库复制模板。standalone/server 按实际中转支持的模型填写 `models` 与 `default_model`。删除不使用的 family 占位条目，避免遮蔽默认凭据。YAML 不支持 `${ENV_VAR}` 插值；需要环境回退时将对应字段留空。

forward 最小配置：

```yaml
mode: forward
forward:
  url: https://images.example.com
  api_key: ""
  timeout: 15m
```

将示例域名替换为已验证的实际服务 URL；URL 允许路径前缀，不允许用户信息、查询参数、片段。真实服务密钥由宿主传入 `IMAGES_MCP_FORWARD_KEY`，也支持 `FORWARD_KEY`、`MCP_FORWARD_KEY`（按此顺序）。不要将示例域名当作用户服务。

CLI > 文件 > 环境 > 默认。CLI 为 `-config`、`-mode`、`-listen`、`-server-key`、`-forward-url`、`-forward-key`、`-forward-timeout`。环境包括 `IMAGES_MCP_MODE`、`MCP_LISTEN`、`MCP_SERVER_KEY` / `SERVER_API_KEY`、`IMAGES_MCP_FORWARD_URL`、`IMAGES_MCP_FORWARD_TIMEOUT`。默认 standalone、监听 :8080、forward 超时 15m。

中转 URL/Key 独立按请求 > 模型 > family 配置 > family 环境 > default > 全局环境解析。family 环境为 GEMINI_BASE_URL/GEMINI_API_KEY 与 GPT_IMAGE_BASE_URL/GPT_IMAGE_API_KEY；全局为 OPENAI_BASE_URL/OPENAI_API_KEY。REST/forward 不接受请求中转凭据/协议覆盖。

超时为请求 > 模型 > default.timeout > 顶层 timeout > IMAGE_TIMEOUT > 10m，接受正数 duration 或秒数字符串。全局 output_format 为顶层 > default.output_format > IMAGE_OUTPUT_FORMAT > file，仅支持 file/base64/both。standalone output_dir 为空时回退 IMAGE_OUTPUT_DIR、系统临时目录。宿主更早的超时仍有效。

## 3. 注册客户端

将二进制安装目录加入启动宿主的 PATH 并执行 `command -v images-mcp`。GUI 宿主或 PATH 不确定时用已验证绝对二进制路径。

Codex standalone 示例：

```toml
[mcp_servers.images-mcp]
command = "images-mcp"
args = ["-config", "/absolute/path/images-mcp.yaml"]
```

把示例配置路径替换成步骤 2 的实际绝对路径。forward 可用同样条目加载 mode: forward 配置，也可显式设置 `-mode forward -forward-url <实际URL>`。

其他宿主在其实际支持的 MCP 注册入口合并：

```json
{"mcpServers":{"images-mcp":{"command":"images-mcp","args":["-config","/absolute/path/images-mcp.yaml"]}}}
```

禁止把 server REST URL 注册成远程 MCP。重启宿主并重新加载工具。

## 4. 本地与服务验证

执行 `test -x "$ABS_BIN"`、`test -r "$ABS_CONFIG"`、`"$ABS_BIN" -h`。通过宿主或具备初始化握手的 MCP 客户端连接 stdio：先 initialize、notifications/initialized，再 tools/list、tools/call list_models。工具必须恰为 generate_image/list_models，不包含 upload_image。

list_models 返回数组，包含 id、description、可选 notes、supports_edit、is_default，不包含凭据。逐项核对配置模型与默认模型。forward 会代理远端列表。

server 可先本地运行并探活：

```sh
images-mcp -config /absolute/path/images-mcp.yaml -mode server -listen 127.0.0.1:8080
curl --fail http://127.0.0.1:8080/healthz
```

探活免鉴权；模型与生成端点在配置服务密钥后需要 Bearer 或 X-API-Key，请通过安全环境注入实际密钥，不打印。公开服务使用强密钥与 HTTPS 反向代理。

只有用户授权生成时调用：

```json
{"prompt":"水彩风格的山间风景","timeout":"15m"}
```

编辑添加有序 `input_images`：第一张主图，其余参考。standalone/forward 直接使用客户端本地绝对路径，forward 自动读取发送。仅 Images 模型使用 size/quality。未配默认模型时传 model。

成功要求 isError 不为 true，结果含 model、非空 images、mime_type 和正数 bytes。standalone file/both 含存在可读的绝对 path；server/forward file/both 含 URL 且无 path。base64 不落盘，both 同时含 Base64。含 Base64 时提供原生 MCP 图像内容。

## 5. REST 输入限制与自愈

POST /v1/generate 接受 JSON 或 multipart，字段为 prompt/model/size/quality/timeout/input_images。未知字段被拒绝。REST 不接受普通本地路径：改由客户端运行 forward，或由调用程序自行构建 multipart。

multipart 使用 request JSON 或独立字段，不混用；独立 input_images 是 JSON 数组。文件用 part:file_N 引用，按 input_images 顺序恢复，拒绝重复字段、缺失引用、未引用文件。PNG/JPEG/WebP，单图 32 MiB、总图 64 MiB、最多 16 张、请求体 96 MiB；参数字段合计 1 MiB。逐 part 有界解析，不写参考图临时文件，不单独上传参考图到 S3。

| 问题 | 修复 |
| --- | --- |
| 子进程秒退/初始化超时 | 验证 PATH、可执行权限、配置可读性与绝对路径，确认运行 standalone 或 forward；重启宿主 |
| forward URL/超时启动错误 | 使用实际 HTTP(S) URL，无用户信息/查询/片段；超时必须为正 |
| 模型/协议错误 | 核对实际模型 ID 与显式 api，重启并调用 list_models |
| 400 图片错误 | 根据 input_images 索引修复路径/URL/MIME；转换真实文件格式，不能只改扩展名 |
| 413 | 减小图片数量/大小与请求体，不自动重试 |
| 401/403 | 安全配置真实服务或中转密钥，排查高优先级字段遮蔽，不猜测/输出密钥 |
| 慢速生成超时 | 配置模型/request 15m、forward 超时与宿主预算；不自动重复生成 |
| list_models 空数组 | 核对 server 配置绝对路径与 models，不解释为中转没有模型 |
| server file/both 启动失败 | 补齐 S3 配置；禁止擅自改输出格式绕过错误 |

错误 JSON 为 error 字符串。400 参数/MIME、401 鉴权、405 方法、413 大小、500 中转/存储。修复后先本地验证，不自动请求真实生成。

## S3 与 Docker

server file/both 必须配置已存在桶与完整静态凭据，支持 MinIO/RustFS/R2/AWS。凭据需 PutObject/GetObject/DeleteObject；R2 通常 region auto，自建存储配置 endpoint/path-style。配置优先于环境，字段与回退见 [README](README.md#s3-与输出) 和模板。服务密钥、中转密钥、存储密钥彼此独立。多图失败清理已上传对象并报告删除错误。

URL 为公开/CDN 前缀或预签名 GET，默认 24h、最长 168h；临时凭据可能提前过期。base64 纯内存，无需 S3；forward 不下载生成 URL、不落地生成文件、不重试。

镜像 `ghcr.io/azicen/images-mcp:latest` 支持 amd64/arm64，生产固定版本或摘要；Debian Bookworm Slim，UID/GID 10001。真实配置主机绝对路径必须可由该用户读取，只读挂载。不要复制密钥进镜像或输出含密钥的 docker inspect。

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

先安全配置实际环境值与可读配置文件；缺失时只做本地构建/语法验证。默认 CMD 为 -mode server -listen :8080，自定义参数会替换整个 CMD，须保留模式与监听。更新配置后重建容器、重连宿主。stdio 容器使用 -i --no-healthcheck 且不分配 TTY。

## 开发验收

运行 `go test -race -cover ./...`（覆盖率超过 85%）、`go vet ./...`、`go build -o bin/images-mcp .`。测试使用本地模拟中转和 S3，禁止真实计费生成。
