# images-mcp：Agent 安装、配置与自愈

适用 Codex、Claude Code、Hermes、Cursor、Cline、Roo Code。架构以 [DESIGN.md](DESIGN.md) 与 [已确认设计](docs/superpowers/specs/2026-10-31-forward-rest-design.md) 为准。

## 硬规则

1. `command` 使用宿主 PATH 中已验证的 `images-mcp` 或可执行二进制绝对路径，禁止依赖 CWD 的相对路径。`-config` 必须是实际绝对路径，不含 `~`、未展开变量或占位符。不传配置路径时不自动发现当前目录文件。
2. 禁止猜测、打印、提交凭据或用占位值请求真实服务。缺失时继续不依赖凭据的本地构建/验证，并提示用户安全配置中转 URL、Key、实际可用模型 ID。
3. 模型显式声明 `api: chat` 或 `api: images`。不臆测模型可用性。
4. 合并客户端配置，保留其他 MCP 条目。修改配置后重启服务与宿主客户端。
5. `list_models` 只验证配置及连通性，不验证中转/S3权限。仅在用户授权的生成任务中执行生成，不自动重试可能计费的请求，不自动换模型。
6. 本地参考图直接传绝对路径；远端部署时在 Agent 所在机器运行 forward，由程序读取并发送文件。禁止让 Agent 编写大段 Base64、禁止伪造服务端本地路径。

## 步骤 1：获取二进制

先核对 [go.mod](go.mod) 的工具链要求：

```sh
set -eu
command -v go >/dev/null
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

当前仓库要求 Go 1.27.1（以所安装版本的 `go.mod` 为准）。要求安装成功且 `-h` 退出 0。源码构建备选方案如下；在选定的父目录执行，已有同名目录时停止，先核对其来源，禁止覆盖：

```sh
set -eu
command -v git >/dev/null
command -v go >/dev/null
go version
test ! -e images-mcp
git clone https://github.com/azicen/images-mcp.git images-mcp
cd images-mcp
# 先核对本次克隆的 go.mod 工具链要求
cat go.mod
go build -o bin/images-mcp .
ABS_BIN="$(cd bin && pwd -P)/images-mcp"
test -x "$ABS_BIN"
"$ABS_BIN" -h
```

下文在同一 Shell 中沿用 `ABS_BIN`；另开 Shell 时重新设置为已验证的实际绝对路径。

## 步骤 2：生成配置文件

- standalone（默认）：本地 stdio MCP，直接调用中转，本地 file/both 返回绝对路径。
- server：纯 REST 服务，只有 `GET /healthz`、`GET /v1/models`、`POST /v1/generate`。
- forward：本地 stdio MCP，代理 REST，自动读取本地参考图并流式发送 multipart。只需远端 URL、服务密钥和超时，不初始化中转/S3。

选择实际绝对配置目录，禁止覆盖已有文件：

```sh
set -eu
umask 077
mkdir -p "$HOME/.config/images-mcp"
ABS_CONFIG_DIR="$(cd "$HOME/.config/images-mcp" && pwd -P)"
ABS_CONFIG="$ABS_CONFIG_DIR/images-mcp.yaml"
if [ ! -e "$ABS_CONFIG" ]; then
  curl --fail --location https://raw.githubusercontent.com/azicen/images-mcp/main/images-mcp.example.yaml -o "$ABS_CONFIG"
fi
chmod 600 "$ABS_CONFIG"
```

源码安装也可在已确认的源码根目录用以下命令替代下载步骤（沿用上面的目录变量，不覆盖现有文件）：

```sh
if [ ! -e "$ABS_CONFIG" ]; then
  (set -C; cat images-mcp.example.yaml > "$ABS_CONFIG")
fi
chmod 600 "$ABS_CONFIG"
test -r "$ABS_CONFIG"
```

standalone 最小配置（模型名取自模板，仅供结构示例；必须按实际中转支持情况替换/删除，不能据此认定可用）：

```yaml
mode: standalone
default_model: gemini-3.1-flash-image
output_format: file
default:
  base_url: "" # 安全配置实际 URL，或回退 OPENAI_BASE_URL
  api_key: "" # 由宿主安全注入 OPENAI_API_KEY
models:
  gemini-3.1-flash-image:
    api: chat
    description: Chat 协议图片模型
  gpt-image-2:
    api: images
    description: Images 协议图片模型
```

server 使用同样的中转与模型配置，改为 `mode: server`，另配 `server.listen`、`server.api_key`；file/both 必须补齐 S3，详见 Docker 章节。server 不作为 MCP 子进程注册。

standalone/server 按实际中转支持的模型填写 `models` 与 `default_model`。删除不使用的 family 占位条目，避免遮蔽默认凭据。YAML 不支持 `${ENV_VAR}` 插值；需要环境回退时将对应字段留空。

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

## 步骤 3：注册 MCP 客户端

将二进制安装目录加入启动宿主的 PATH 并执行 `command -v images-mcp`。GUI 宿主或 PATH 不确定时用已验证绝对二进制路径。

Codex standalone 示例：

```toml
[mcp_servers.images-mcp]
command = "images-mcp"
args = ["-config", "/absolute/path/images-mcp.yaml"]
```

把示例配置路径替换成步骤 2 的实际绝对路径。forward 可用同样条目加载 mode: forward 配置，也可显式设置 `-mode forward -forward-url <实际URL>`。

Claude Code、Cursor、Cline、Roo Code、Hermes 的标准 JSON 配置结构如下（standalone）。在各宿主实际支持的本地 stdio 注册入口合并 `mcpServers`；若入口使用单条 server 对象，则取其中 `images-mcp` 的内容，保留其他条目：

```json
{"mcpServers":{"images-mcp":{"command":"images-mcp","args":["-config","/absolute/path/images-mcp.yaml"]}}}
```

forward 模式的标准 JSON（配置文件采用步骤 2 的 forward 最小配置）：

```json
{
  "mcpServers": {
    "images-mcp": {
      "command": "images-mcp",
      "args": ["-config", "/absolute/path/images-mcp.yaml", "-mode", "forward"]
    }
  }
}
```

Codex forward 模式，在其 `config.toml` 中合并：

```toml
[mcp_servers.images-mcp]
command = "images-mcp"
args = ["-config", "/absolute/path/images-mcp.yaml", "-mode", "forward"]
```

所有示例的 `/absolute/path/images-mcp.yaml` 均须替换成 `ABS_CONFIG` 的实际值；JSON/TOML 不会展开 Shell 变量。`command` 优先使用已验证的 `"images-mcp"`，PATH 不确定时替换成 `ABS_BIN` 的实际值。密钥由宿主安全环境传入，不把真实密钥粘贴到示例或聊天中。

客户端统一连接本地 stdio MCP。禁止把 server REST URL 注册成远程 MCP。重启服务与宿主并重新加载工具。

## 步骤 4：验证服务与工具连通

执行 `test -x "$ABS_BIN"`、`test -r "$ABS_CONFIG"`、`"$ABS_BIN" -h`。通过宿主或具备初始化握手的 MCP 客户端连接 stdio：先 initialize、notifications/initialized，再 tools/list、tools/call list_models。工具必须恰为 generate_image/list_models，不包含 upload_image。

list_models 返回数组，包含 id、description、可选 notes、supports_edit、is_default，不包含凭据。逐项核对配置模型与默认模型。forward 会代理远端列表。

Python 3 自动化握手测试如下。在同一 Shell 中执行，使用已验证的 `ABS_BIN`、`ABS_CONFIG`；配置须为 standalone 或 forward。脚本只调用 `list_models`，不生成图片、不验证中转/S3 权限。每次响应等待最多 15 秒（测试上限，可按网络情况调整）；stderr 丢弃以免自动回显敏感诊断信息，失败时退出非零。使用逐行 JSON-RPC，无 Content-Length 头：

```sh
python3 - "$ABS_BIN" "$ABS_CONFIG" <<'PY'
import json
import os
import queue
import subprocess
import sys
import threading
import time

binary, config = sys.argv[1:]
assert os.path.isabs(binary) and os.access(binary, os.X_OK)
assert os.path.isabs(config) and os.access(config, os.R_OK)
proc = subprocess.Popen(
    [binary, "-config", config],
    stdin=subprocess.PIPE, stdout=subprocess.PIPE,
    stderr=subprocess.DEVNULL, text=True, encoding="utf-8",
)
lines = queue.Queue()

def read_stdout():
    try:
        for line in proc.stdout:
            lines.put(line)
    finally:
        lines.put(None)

reader = threading.Thread(target=read_stdout, daemon=True)
reader.start()

def send(message):
    proc.stdin.write(json.dumps({"jsonrpc": "2.0", **message}) + "\n")
    proc.stdin.flush()

def call(request_id, method, params):
    send({"id": request_id, "method": method, "params": params})
    deadline = time.monotonic() + 15
    while True:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise TimeoutError("MCP response timeout")
        line = lines.get(timeout=remaining)
        if line is None:
            raise RuntimeError("MCP subprocess closed stdout")
        message = json.loads(line)
        if message.get("id") != request_id:
            continue  # Ignore server notifications.
        assert message.get("jsonrpc") == "2.0"
        assert "error" not in message, "MCP request failed"
        return message["result"]

try:
    result = call(1, "initialize", {
        "protocolVersion": "2025-03-26",
        "capabilities": {},
        "clientInfo": {"name": "images-mcp-check", "version": "1.0"},
    })
    assert result["serverInfo"]["name"] == "images-mcp"
    send({"method": "notifications/initialized"})
    tools = call(2, "tools/list", {})["tools"]
    assert len(tools) == 2
    assert {tool["name"] for tool in tools} == {"generate_image", "list_models"}
    result = call(3, "tools/call", {"name": "list_models", "arguments": {}})
    assert result.get("isError") is not True
    texts = [item["text"] for item in result["content"] if item["type"] == "text"]
    assert len(texts) == 1
    models = json.loads(texts[0])
    assert isinstance(models, list)
    for model in models:
        assert isinstance(model, dict)
        assert isinstance(model.get("id"), str) and model["id"]
        assert isinstance(model.get("description"), str)
        assert isinstance(model.get("supports_edit"), bool)
        assert isinstance(model.get("is_default"), bool)
        assert "notes" not in model or isinstance(model["notes"], str)
    print(json.dumps({"status": "ok", "models": models}, ensure_ascii=False))
finally:
    proc.stdin.close()
    try:
        proc.wait(timeout=3)
    except subprocess.TimeoutExpired:
        proc.terminate()
        try:
            proc.wait(timeout=3)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()
    reader.join(timeout=3)
    proc.stdout.close()
PY
```

成功时脚本退出码为 0，输出 `{"status": "ok", "models": [...]}`。空数组是有效 JSON，但需按步骤 5 检查配置；逐项核对模型与默认标志后才能宣告配置符合用户需求。

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

工具调用示例（仅在用户授权后执行，使用已配置的默认模型）：

```json
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"generate_image","arguments":{"prompt":"保留主体，将背景改为水彩山景","timeout":"15m","input_images":["/absolute/path/source.png"]}}}
```

本地路径必须替换为实际存在可读的图片。成功 MCP 结果的文本内容为如下业务 JSON（示意数据，不表示已执行生成）：

```json
{"model":"configured-model-id","images":[{"path":"/absolute/output/generated.png","mime_type":"image/png","bytes":1024}]}
```

server/forward 的 file/both 对应结果使用 URL：

```json
{"model":"configured-model-id","images":[{"url":"https://storage.example.com/generated.png","mime_type":"image/png","bytes":1024}]}
```

成功要求 isError 不为 true，结果含 model、非空 images、mime_type 和正数 bytes。standalone file/both 含存在可读的绝对 path；server/forward file/both 含 URL 且无 path。base64 不落盘，both 同时含 Base64。含 Base64 时提供原生 MCP 图像内容。

## 步骤 5：排障与自愈矩阵 (Troubleshooting & Self-Healing Matrix)

POST /v1/generate 接受 JSON 或 multipart，字段为 prompt/model/size/quality/timeout/input_images。未知字段被拒绝。REST 不接受普通本地路径：改由客户端运行 forward，或由调用程序自行构建 multipart。

multipart 使用 request JSON 或独立字段，不混用；独立 input_images 是 JSON 数组。文件用 part:file_N 引用，按 input_images 顺序恢复，拒绝重复字段、缺失引用、未引用文件。PNG/JPEG/WebP，单图 32 MiB、总图 64 MiB、最多 16 张、请求体 96 MiB；参数字段合计 1 MiB。逐 part 有界解析，不写参考图临时文件，不单独上传参考图到 S3。

| Symptom | Cause | Fix |
| --- | --- | --- |
| 子进程秒退 / 15s 初始化超时 | PATH、权限、配置路径错误，或误用 server | 验证 `command -v images-mcp`、可执行权限、配置可读性和绝对路径；使用 standalone/forward，完成初始化握手后重启宿主 |
| forward 连接失败 / 401 Unauthorized | URL、网络、服务密钥或高优先级配置错误 | 核对实际 HTTP(S) URL 与路径前缀，禁止用户信息/查询/片段；先探活 `/healthz`，再安全注入真实 forward key，排查字段遮蔽，不打印密钥 |
| Unknown model family / 路由启动报错 | 模型协议缺失、无效或模型 ID 配错 | 核对实际可用模型 ID，逐模型声明 `api: chat` 或 `api: images`，重启并调用 `list_models`，不自动换模型 |
| 慢速模型请求超时 | 模型、forward 或宿主超时预算不足 | 将请求/模型 timeout 配为 `15m`，同步配置 forward.timeout 与宿主预算；已有生成可能计费，不自动重试 |
| 图片加载失败 / MIME 不受支持 | 路径不可读、URL 失败或文件实际格式不符 | 按 `input_images` 索引核对客户端本地绝对路径；远端部署使用本地 forward；将真实文件转换为 PNG/JPEG/WebP，不能只改扩展名 |
| 413 超过大小限制 | 单图或总图、数量、请求体超限 | 单图 ≤32 MiB、总图 ≤64 MiB、最多 16 张、请求体 ≤96 MiB、参数字段合计 ≤1 MiB；缩小文件或数量，不自动重试 |
| `list_models` 返回空数组 | 加载了错误配置或 `models` 为空 | 核对实际绝对配置路径和 models；forward 检查远端 server 配置并重启，不解释为中转没有模型 |
| server 模式启动报 S3 错误 | file/both 缺少 bucket 或完整静态凭据 | 补齐已存在 bucket、access key、secret key、region，按提供商配置 endpoint/path-style；禁止擅自改输出格式绕过错误 |
| 401/403 中转或存储错误 | 凭据错误或权限不足 | 安全配置真实凭据，检查高优先级字段遮蔽及 PutObject/GetObject/DeleteObject 权限；不猜测、不输出凭据 |

错误 JSON 为 error 字符串。400 参数/MIME、401 鉴权、405 方法、413 大小、500 中转/存储。修复后先本地验证，不自动请求真实生成。

## Docker 部署（Server 模式）与 S3

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
