# images-mcp：Agent 安装、配置与自愈

本文供 Codex、Claude Code、Claude Desktop、Cursor、Cline、Roo Code、Hermes 等 Agent 按步骤执行。架构以 [DESIGN.md](DESIGN.md) 和 [已确认设计](docs/superpowers/specs/2026-10-31-forward-rest-design.md) 为准，面向用户的说明见 [README](README.md)。

## 硬规则

每一步都必须满足以下规则，违反任何一条都应停下来修正，而不是继续往下做。

1. **路径必须是真实的绝对路径。** `command` 填宿主 PATH 中已验证的 `images-mcp`，或可执行二进制的绝对路径，不能用依赖当前目录的相对路径。`-config` 的值同样必须是绝对路径，不能含 `~`、未展开的变量或占位符。不传 `-config` 时程序不会自动读取当前目录下的配置文件。
2. **不碰凭据。** 不猜测、不打印、不提交凭据，也不拿占位值去请求真实服务。缺凭据时，先完成不依赖凭据的本地构建和验证，再提醒用户自行安全配置中转 URL、Key 和实际可用的模型 ID。
3. **模型协议要显式声明。** 每个模型都写明 `api: chat` 或 `api: images`，不要臆测哪些模型可用。
4. **合并而非覆盖客户端配置。** 保留其他 MCP 条目。改完配置后重启服务和宿主客户端。
5. **生成要有授权。** `list_models` 只能证明配置已加载、工具能连通，不能证明中转或 S3 权限正常。只有用户明确要求生成时才调用 `generate_image`；可能计费的请求失败后不自动重试，也不擅自换模型。
6. **参考图传本地路径。** 本地参考图直接传绝对路径。服务部署在远端时，在 Agent 所在机器上运行 forward，由程序读取并发送文件。不要让 Agent 手写大段 Base64，也不要编造服务端本地路径。

## 步骤 1：获取二进制

三种方式任选其一，优先使用预编译二进制。无论哪种方式，最后都要得到一个可执行的绝对路径 `ABS_BIN`，并且 `"$ABS_BIN" -h` 退出码为 0。

### 方式 A：下载预编译二进制（推荐）

[GitHub Releases](https://github.com/azicen/images-mcp/releases) 为每个版本提供 CGO 关闭的静态二进制，并附带 `checksums.txt`（SHA-256）：

| 系统 | amd64 | arm64 |
| --- | --- | --- |
| Linux | `images-mcp-linux-amd64.tar.gz` | `images-mcp-linux-arm64.tar.gz` |
| macOS | `images-mcp-darwin-amd64.tar.gz` | `images-mcp-darwin-arm64.tar.gz` |
| Windows | `images-mcp-windows-amd64.zip` | `images-mcp-windows-arm64.zip` |

压缩包里只有一个带平台后缀的文件（如 `images-mcp-linux-amd64`、`images-mcp-windows-amd64.exe`），解压后需改名为 `images-mcp`（Windows 为 `images-mcp.exe`）。

Linux / macOS：下面的脚本自动识别平台，在新建的空临时目录中下载并校验，然后安装到 `$HOME/.local/bin`。目标位置已有同名文件时脚本会停止，先确认它的来源和版本，不要直接覆盖。

```sh
set -eu
case "$(uname -s)" in
  Linux) OS=linux ;;
  Darwin) OS=darwin ;;
  *) echo "unsupported OS: $(uname -s)" >&2; exit 1 ;;
esac
case "$(uname -m)" in
  x86_64|amd64) ARCH=amd64 ;;
  aarch64|arm64) ARCH=arm64 ;;
  *) echo "unsupported arch: $(uname -m)" >&2; exit 1 ;;
esac
NAME="images-mcp-${OS}-${ARCH}"
BASE="https://github.com/azicen/images-mcp/releases/latest/download"
INSTALL_DIR="$HOME/.local/bin"
mkdir -p "$INSTALL_DIR"
ABS_BIN="$(cd "$INSTALL_DIR" && pwd -P)/images-mcp"
test ! -e "$ABS_BIN"
WORK="$(mktemp -d)"
curl --fail --location -o "$WORK/$NAME.tar.gz" "$BASE/$NAME.tar.gz"
curl --fail --location -o "$WORK/checksums.txt" "$BASE/checksums.txt"
EXPECTED="$(awk -v f="$NAME.tar.gz" '$2 == f {print $1}' "$WORK/checksums.txt")"
test -n "$EXPECTED"
if command -v sha256sum >/dev/null; then
  ACTUAL="$(sha256sum "$WORK/$NAME.tar.gz" | awk '{print $1}')"
else
  ACTUAL="$(shasum -a 256 "$WORK/$NAME.tar.gz" | awk '{print $1}')"
fi
test "$EXPECTED" = "$ACTUAL"
tar -xzf "$WORK/$NAME.tar.gz" -C "$WORK" "$NAME"
install -m 755 "$WORK/$NAME" "$ABS_BIN"
rm -rf "$WORK"
test -x "$ABS_BIN"
"$ABS_BIN" -h
```

需要固定版本时，把 `BASE` 换成 `https://github.com/azicen/images-mcp/releases/download/<tag>`。确保 `$HOME/.local/bin` 在宿主客户端的 PATH 中，否则注册时使用 `ABS_BIN` 的实际值。

Windows（PowerShell）：

```powershell
$ErrorActionPreference = 'Stop'
$Arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$Name = "images-mcp-windows-$Arch"
$Base = 'https://github.com/azicen/images-mcp/releases/latest/download'
$InstallDir = Join-Path $env:LOCALAPPDATA 'Programs\images-mcp'
$AbsBin = Join-Path $InstallDir 'images-mcp.exe'
if (Test-Path $AbsBin) { throw "$AbsBin already exists; verify it before replacing" }
$Work = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Path $Work, $InstallDir -Force | Out-Null
Invoke-WebRequest "$Base/$Name.zip" -OutFile "$Work\$Name.zip"
Invoke-WebRequest "$Base/checksums.txt" -OutFile "$Work\checksums.txt"
$Expected = (Get-Content "$Work\checksums.txt" | Where-Object { $_ -match "\s$Name\.zip$" }) -split '\s+' | Select-Object -First 1
$Actual = (Get-FileHash "$Work\$Name.zip" -Algorithm SHA256).Hash
if (-not $Expected -or $Expected -ne $Actual.ToLower()) { throw 'checksum mismatch' }
Expand-Archive "$Work\$Name.zip" -DestinationPath $Work
Move-Item "$Work\$Name.exe" $AbsBin
Remove-Item $Work -Recurse
& $AbsBin -h
```

Windows 下 JSON 配置中的路径需写成 `C:\\Users\\...\\images-mcp.exe` 或 `C:/Users/.../images-mcp.exe`。

### 方式 B：go install

先核对 [go.mod](go.mod) 的工具链要求（当前为 Go 1.27.1，以所安装版本的 `go.mod` 为准）：

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

### 方式 C：源码构建

在选定的父目录中执行。已有同名目录时停下，先核对它的来源，不要覆盖：

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

后续步骤在同一个 Shell 中沿用 `ABS_BIN`。换了 Shell 就重新设置为已验证的实际绝对路径。

## 步骤 2：选择模式并生成配置

images-mcp 有两种用法，先按用户场景选定一种：

| 用法 | 涉及的模式 | 适用场景 | 本地需要的配置 |
| --- | --- | --- | --- |
| 本地直连 | `standalone`（默认） | 个人使用，本机能访问中转 | 中转 URL、Key、模型列表 |
| 远程服务 + 本地代理 | 远端 `server` + 本地 `forward` | 团队共享、多台机器、中转 Key 统一托管 | 远端服务 URL、服务密钥、超时 |

- **standalone**：本地 stdio MCP，直接请求中转。`file` / `both` 输出时把图片写到本地并返回绝对路径。
- **server**：只提供 REST API，即 `GET /healthz`、`GET /v1/models`、`POST /v1/generate`，部署在远端，不能注册为 MCP。
- **forward**：本地 stdio MCP，把工具调用转成对 server 的 REST 请求，并自动读取本地参考图、以 multipart 流式发送。它只需要远端 URL、服务密钥和超时，不会初始化中转或 S3。

两种用法里，Agent 客户端连接的始终是本地 stdio 进程（standalone 或 forward）。

### 创建配置文件

选定实际的绝对配置目录，不要覆盖已有文件：

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

源码安装时，也可以在已确认的源码根目录用下面的命令代替下载（沿用上面的目录变量，同样不覆盖已有文件）：

```sh
if [ ! -e "$ABS_CONFIG" ]; then
  (set -C; cat images-mcp.example.yaml > "$ABS_CONFIG")
fi
chmod 600 "$ABS_CONFIG"
test -r "$ABS_CONFIG"
```

模板包含全部选项。按所选用法裁剪成下面的最小配置。

### 用法一：本地直连（standalone）

以下模型名取自模板，只用来示意结构，不代表用户的中转一定提供。必须按中转实际支持的模型替换或删除：

```yaml
mode: standalone
default_model: gemini-3.1-flash-image
output_format: file
default:
  base_url: "" # 填实际 URL，或留空回退 OPENAI_BASE_URL
  api_key: "" # 留空，由宿主安全注入 OPENAI_API_KEY
models:
  gemini-3.1-flash-image:
    api: chat
    description: Chat 协议图片模型
  gpt-image-2:
    api: images
    description: Images 协议图片模型
```

`models` 和 `default_model` 按中转实际支持的模型填写。不用的 family 占位条目要删掉，否则会遮蔽默认凭据。YAML 不支持 `${ENV_VAR}` 插值，需要从环境变量读取时把对应字段留空即可。

### 用法二：远程服务 + 本地代理（server + forward）

**远端 server**：中转和模型配置与 standalone 相同，改成 `mode: server`，再配置 `server.listen` 和 `server.api_key`（服务密钥）。输出为 `file` / `both` 时还必须配好 S3，见下文“Docker 部署与 S3”。server 不能作为 MCP 子进程注册。

**本地 forward**：

```yaml
mode: forward
forward:
  url: https://images.example.com
  api_key: ""
  timeout: 15m
```

把示例域名换成已验证的实际服务地址，不要把示例域名当成用户的服务。URL 可以带路径前缀，但不能带用户信息、查询参数或片段。服务密钥由宿主通过 `IMAGES_MCP_FORWARD_KEY` 注入，也支持 `FORWARD_KEY`、`MCP_FORWARD_KEY`（按此顺序查找）。

### 配置优先级

- **运行参数**：CLI > 配置文件 > 环境变量 > 默认值。
  - CLI 参数：`-config`、`-mode`、`-listen`、`-server-key`、`-forward-url`、`-forward-key`、`-forward-timeout`。
  - 环境变量：`IMAGES_MCP_MODE`、`MCP_LISTEN`、`MCP_SERVER_KEY` / `SERVER_API_KEY`、`IMAGES_MCP_FORWARD_URL`、`IMAGES_MCP_FORWARD_TIMEOUT`。
  - 默认值：standalone 模式、监听 `:8080`、forward 超时 15m。
- **中转 URL / Key**：两者各自按 请求 > 模型 > family 配置 > family 环境变量 > `default` > 全局环境变量 解析。family 环境变量是 `GEMINI_BASE_URL` / `GEMINI_API_KEY` 和 `GPT_IMAGE_BASE_URL` / `GPT_IMAGE_API_KEY`，全局环境变量是 `OPENAI_BASE_URL` / `OPENAI_API_KEY`。REST 和 forward 不接受请求级的中转凭据或协议覆盖。
- **超时**：请求 > 模型 > `default.timeout` > 顶层 `timeout` > `IMAGE_TIMEOUT` > 10m。接受正数 duration（如 `15m`）或秒数字符串。宿主自身更短的超时依然生效。
- **输出格式**：顶层 `output_format` > `default.output_format` > `IMAGE_OUTPUT_FORMAT` > `file`，只支持 `file` / `base64` / `both`。
- **输出目录**（standalone）：`output_dir` 为空时依次回退到 `IMAGE_OUTPUT_DIR`、系统临时目录。

## 步骤 3：注册 MCP 客户端

先把二进制所在目录加入宿主的 PATH，并用 `command -v images-mcp` 确认。GUI 宿主或拿不准 PATH 时，`command` 直接填 `ABS_BIN` 的实际值。

各客户端的配置位置：

| 客户端 | 配置位置 | 格式 |
| --- | --- | --- |
| Claude Code | `claude mcp add` 命令；或项目根目录的 `.mcp.json`（项目级） | JSON `mcpServers` |
| Claude Desktop | macOS `~/Library/Application Support/Claude/claude_desktop_config.json`；Windows `%APPDATA%\Claude\claude_desktop_config.json` | JSON `mcpServers` |
| Cursor | 全局 `~/.cursor/mcp.json`；项目 `.cursor/mcp.json` | JSON `mcpServers` |
| Cline | `cline_mcp_settings.json`（在 Cline 的 MCP Servers 面板点 Configure 打开） | JSON `mcpServers` |
| Roo Code | 全局 `mcp_settings.json`（在 Roo Code 的 MCP 面板打开）；项目 `.roo/mcp.json` | JSON `mcpServers` |
| Codex | `~/.codex/config.toml` | TOML `[mcp_servers.*]` |
| Hermes | `~/.hermes/config.yaml` 的 `mcp_servers` 键 | YAML，字段同为 `command` / `args` |

文件路径以宿主当前版本的文档为准；位置对不上时，从宿主自己的 MCP 设置入口打开配置文件，不要凭空新建。

### Claude Code 命令行

在步骤 1、2 所在的 Shell 中执行，变量会被展开成实际绝对路径。`--scope user` 对当前用户的所有项目生效，去掉则只对当前项目生效：

```sh
# 本地直连
claude mcp add --scope user images-mcp -- "$ABS_BIN" -config "$ABS_CONFIG"
# 本地代理（配置文件为 forward 最小配置）
claude mcp add --scope user images-mcp -- "$ABS_BIN" -config "$ABS_CONFIG" -mode forward
claude mcp list
```

同名条目已存在时，先用 `claude mcp get images-mcp` 查看，确认可以替换后再 `claude mcp remove images-mcp` 并重新添加。

### JSON 配置（Claude Code `.mcp.json`、Claude Desktop、Cursor、Cline、Roo Code）

本地直连：

```json
{"mcpServers":{"images-mcp":{"command":"images-mcp","args":["-config","/absolute/path/images-mcp.yaml"]}}}
```

本地代理：

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

把 `images-mcp` 条目合并进已有的 `mcpServers`，不要动其他条目。

### TOML 配置（Codex）

在 `~/.codex/config.toml` 中合并：

```toml
[mcp_servers.images-mcp]
command = "images-mcp"
args = ["-config", "/absolute/path/images-mcp.yaml"]
```

本地代理时 `args` 改为 `["-config", "/absolute/path/images-mcp.yaml", "-mode", "forward"]`。也可以不用配置文件，直接写 `-mode forward -forward-url <实际URL>`。

### YAML 配置（Hermes）

```yaml
mcp_servers:
  images-mcp:
    command: images-mcp
    args: ["-config", "/absolute/path/images-mcp.yaml"]
```

本地代理时同样在 `args` 末尾追加 `"-mode", "forward"`。

### 注意事项

- 所有示例里的 `/absolute/path/images-mcp.yaml` 都要换成 `ABS_CONFIG` 的实际值。JSON、TOML、YAML 都不会展开 Shell 变量。
- `command` 优先填已验证的 `images-mcp`，PATH 不确定时换成 `ABS_BIN` 的实际值。
- 密钥通过宿主的安全环境传入，不要把真实密钥写进示例或贴到聊天里。
- 不要把 server 的 REST URL 注册成远程 MCP。
- 保存后重启宿主客户端并重新加载工具。

## 步骤 4：验证服务与工具连通

先执行 `test -x "$ABS_BIN"`、`test -r "$ABS_CONFIG"`、`"$ABS_BIN" -h`。然后通过宿主，或任何支持初始化握手的 MCP 客户端连接 stdio：依次发送 `initialize`、`notifications/initialized`，再调用 `tools/list` 和 `tools/call list_models`。工具必须恰好是 `generate_image` 和 `list_models` 两个，不能出现 `upload_image`。

`list_models` 返回一个数组，每项字段如下，不含任何凭据：

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `id` | 字符串 | 必有 |
| `description` | 字符串 | 必有 |
| `supports_edit` | 布尔 | 必有 |
| `is_default` | 布尔 | 必有 |
| `notes` | 字符串 | 可选 |
| `tags` | 字符串数组 | 可选，供 Agent 按标签推荐和选择模型 |
| `qualities` / `default_quality` | 字符串数组 / 字符串 | 可选，质量档位与默认档位 |
| `sizes` / `default_size` | 字符串数组 / 字符串 | 可选，预设尺寸与默认尺寸 |
| `supports_custom_size` | 布尔 | 可选，省略表示 false |

逐项核对模型和默认模型是否与配置一致。forward 模式下返回的是远端 server 的列表。

返回示例（不代表模型已验证可用）：

```json
[{"id":"gemini-3.1-flash-image","description":"图片模型","tags":["fast","text-rendering"],"supports_edit":true,"is_default":true}]
```

### Python 3 自动化握手测试

在同一个 Shell 中执行，沿用已验证的 `ABS_BIN`、`ABS_CONFIG`，配置须为 standalone 或 forward。脚本只调用 `list_models`，不生成图片，也不验证中转或 S3 权限。每个响应最多等 15 秒（测试上限，可按网络情况调整）。stderr 被丢弃，以免回显敏感诊断信息；任何失败都会以非零码退出。协议为逐行 JSON-RPC，没有 Content-Length 头：

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
        assert "default_size" not in model or isinstance(model["default_size"], str)
        assert "supports_custom_size" not in model or isinstance(model["supports_custom_size"], bool)
        assert "sizes" not in model or (
            isinstance(model["sizes"], list)
            and all(isinstance(size, str) for size in model["sizes"])
        )
        assert "default_quality" not in model or isinstance(model["default_quality"], str)
        assert "qualities" not in model or (
            isinstance(model["qualities"], list)
            and all(isinstance(quality, str) for quality in model["qualities"])
        )
        assert "tags" not in model or (
            isinstance(model["tags"], list)
            and all(isinstance(tag, str) for tag in model["tags"])
        )
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

成功时退出码为 0，并输出 `{"status": "ok", "models": [...]}`。空数组也是合法 JSON，但需要按步骤 5 检查配置；逐项核对模型和默认标志后，才能告诉用户配置符合需求。

### 本地探活 server

```sh
images-mcp -config /absolute/path/images-mcp.yaml -mode server -listen 127.0.0.1:8080
curl --fail http://127.0.0.1:8080/healthz
```

`/healthz` 不需要鉴权。配置了服务密钥后，模型和生成端点需要 `Authorization: Bearer` 或 `X-API-Key` 头；实际密钥通过安全环境注入，不要打印。对外提供服务时使用强密钥，并放在 HTTPS 反向代理之后。

### 生成调用（仅在用户授权后）

```json
{"prompt":"水彩风格的山间风景","timeout":"15m"}
```

**尺寸**。模型通过 `sizes`（预设数组）、`default_size`（默认尺寸）、`supports_custom_size`（是否接受自定义尺寸，默认 false）配置，例如 `sizes: [1024x1024, auto]`、`default_size: 1024x1024`、`supports_custom_size: true`。`list_models` 和 `GET /v1/models` 返回这些可选字段：预设会去掉首尾空白和空项，默认值去掉首尾空白，空值和 false 不输出。请按中转实际能力配置。

- Images 请求不传 size 或只传空白时，使用 `default_size`。预设匹配不区分大小写，并规范为配置里的写法。
- 允许自定义时，`WIDTHxHEIGHT` 的宽高各自对齐到最近的 16 的倍数（正好在中点时向上取，最小 16），例如 `1920x1080` → `1920x1088`。对齐后单边不超过 3840px，长宽比在 1:3 到 3:1 之间，总像素在 655,360 到 8,294,400 之间（都含边界）。
- 格式不对（如 `16:9`、`4k`）、尺寸超限，或模型不支持自定义尺寸却传了非预设值，都直接报错，不会悄悄改用默认值或预设。
- 配置里的预设和默认值视为可信，不做自定义尺寸校验。
- Gemini（`api: chat`）忽略 size。

**质量**。模型通过 `qualities`（档位数组）和 `default_quality`（默认档位）配置，例如 `qualities: [low, medium, high]`、`default_quality: medium`。`list_models` 和 `GET /v1/models` 返回这两个可选字段，返回前去掉首尾空白、过滤空档位，空字段不输出。

- 请求的 `quality` 用于选择 Images 质量档位，例如 GPT Image 的 `low/medium/high/auto`、DALL-E 3 的 `standard/hd`。不传或只传空白时使用配置默认值。
- 配置了有效 `qualities` 时，匹配不区分大小写并规范为配置写法；不在列表中的档位回退到 `default_quality`，没配默认值则用第一个有效档位。
- 没配有效档位列表时，原样透传调用方的值。
- Gemini 忽略 quality。

尺寸调整、质量回退以及参数被忽略时，结果都会带上 `notice` 字段，quality 和 size 的提示用 `; ` 连接。REST、forward、MCP 都会保留这个提示，不会因此重试或换模型。

**编辑**。传入有序的 `input_images`：第一张是主图，其余是参考图。standalone 和 forward 直接使用客户端本地绝对路径，forward 会自动读取并发送。只有 Images 模型使用 size，quality 按上面的规则解析。没有配置默认模型时必须传 `model`。

工具调用示例（仅在用户授权后执行，使用默认模型）：

```json
{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"generate_image","arguments":{"prompt":"保留主体，将背景改为水彩山景","timeout":"15m","input_images":["/absolute/path/source.png"]}}}
```

本地路径必须换成实际存在且可读的图片。成功时，MCP 结果的文本内容是如下业务 JSON（示意数据，不代表已执行生成）：

```json
{"model":"configured-model-id","images":[{"path":"/absolute/output/generated.png","mime_type":"image/png","bytes":1024}]}
```

server / forward 在 `file` / `both` 输出时返回 URL：

```json
{"model":"configured-model-id","images":[{"url":"https://storage.example.com/generated.png","mime_type":"image/png","bytes":1024}]}
```

判定成功的条件：

- `isError` 不为 true；结果包含 `model`、非空的 `images`，每张图都有 `mime_type` 和正数 `bytes`。
- standalone `file` / `both`：含存在且可读的绝对 `path`。
- server / forward `file` / `both`：含 `url`，不含 `path`。
- `base64` 不落盘；`both` 同时包含 Base64。只要含 Base64，结果中就会附带 MCP 原生图像内容。

## 步骤 5：排障与自愈矩阵 (Troubleshooting & Self-Healing Matrix)

`POST /v1/generate` 接受 JSON 或 multipart，字段为 `prompt` / `model` / `size` / `quality` / `timeout` / `input_images`，未知字段会被拒绝。REST 不接受普通本地路径，需要传本地图时，在客户端运行 forward，或由调用程序自己构建 multipart。

multipart 的规则：

- 参数要么全部放在 `request` JSON 字段里，要么全部用独立字段，不能混用。独立的 `input_images` 字段是 JSON 数组。
- 文件用 `part:file_N` 引用，按 `input_images` 顺序还原。重复字段、引用缺失、文件未被引用都会被拒绝。
- 只支持 PNG / JPEG / WebP。单图 ≤32 MiB，总图 ≤64 MiB，最多 16 张，请求体 ≤96 MiB，参数字段合计 ≤1 MiB。
- 逐 part 有界解析，参考图不写临时文件，也不会单独上传到 S3。

| Symptom | Cause | Fix |
| --- | --- | --- |
| 子进程秒退 / 15s 初始化超时 | PATH、权限或配置路径有误，或把 server 当成了 MCP | 检查 `command -v images-mcp`、可执行权限、配置是否可读、路径是否为绝对路径；改用 standalone / forward，完成初始化握手后重启宿主 |
| forward 连接失败 / 401 Unauthorized | URL、网络、服务密钥有误，或被更高优先级的配置覆盖 | 核对实际 HTTP(S) URL 和路径前缀，去掉用户信息、查询参数、片段；先探活 `/healthz`，再安全注入真实 forward key，检查是否被高优先级字段遮蔽，不打印密钥 |
| Unknown model family / 路由启动报错 | 模型缺少协议声明、协议无效或模型 ID 写错 | 核对实际可用的模型 ID，逐个声明 `api: chat` 或 `api: images`，重启后调用 `list_models`，不擅自换模型 |
| 慢速模型请求超时 | 模型、forward 或宿主的超时预算不够 | 把请求或模型的 timeout 设为 `15m`，同步调整 `forward.timeout` 和宿主超时；已发出的生成可能已计费，不自动重试 |
| 图片加载失败 / MIME 不受支持 | 路径不可读、URL 拉取失败，或文件实际格式与扩展名不符 | 按 `input_images` 的序号核对客户端本地绝对路径；远端部署时用本地 forward；把文件真正转换成 PNG / JPEG / WebP，只改扩展名无效 |
| 413 超过大小限制 | 单图、总图、数量或请求体超限 | 单图 ≤32 MiB、总图 ≤64 MiB、最多 16 张、请求体 ≤96 MiB、参数字段合计 ≤1 MiB；压缩图片或减少数量，不自动重试 |
| `list_models` 返回空数组 | 加载了错误的配置，或 `models` 为空 | 核对实际的绝对配置路径和 `models`；forward 模式检查远端 server 的配置并重启。这不代表中转没有模型 |
| server 启动报 S3 错误 | `file` / `both` 输出缺少 bucket 或完整的静态凭据 | 补齐已存在的 bucket、access key、secret key、region，按存储服务配置 endpoint / path-style；不要擅自改输出格式来绕过 |
| 401/403 中转或存储错误 | 凭据错误或权限不足 | 安全配置真实凭据，检查是否被高优先级字段遮蔽，确认 PutObject / GetObject / DeleteObject 权限；不猜测、不输出凭据 |

错误响应是带 `error` 字符串的 JSON。状态码含义：400 参数或 MIME 错误，401 鉴权失败，405 方法不允许，413 超出大小限制，500 中转或存储错误。修复后先做本地验证，不要自动发起真实生成。

## Docker 部署与 S3（server 模式）

server 以 `file` / `both` 输出时，必须配置已存在的桶和完整的静态凭据，支持 MinIO、RustFS、R2、AWS 等。凭据需要 PutObject / GetObject / DeleteObject 权限。R2 的 region 通常填 `auto`，自建存储需配置 endpoint 和 path-style。配置文件优先于环境变量，字段和回退规则见 [README](README.md#远程部署与-docker) 和配置模板。服务密钥、中转密钥、存储密钥相互独立。多图生成中途失败时，会清理已上传的对象并报告删除错误。

返回的 URL 是公开 / CDN 前缀，或预签名 GET 链接（默认有效 24h，最长 168h；使用临时凭据时可能提前失效）。`base64` 输出完全在内存中，不需要 S3。forward 不下载生成结果的 URL、不落地生成文件，也不重试。

镜像 `ghcr.io/azicen/images-mcp:latest` 支持 amd64 / arm64，基于 Debian Bookworm Slim，以 UID/GID 10001 运行。生产环境请固定版本或摘要。宿主机上的配置文件必须能被该用户读取，并以只读方式挂载。不要把密钥复制进镜像，也不要输出包含密钥的 `docker inspect` 结果。

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

运行前先安全配置好实际的环境变量和可读的配置文件；条件不具备时只做本地构建和语法验证。默认 CMD 是 `-mode server -listen :8080`，传入自定义参数会替换整个 CMD，所以必须带上模式和监听地址。更新配置后重建容器，并让宿主重新连接。以 stdio 方式运行容器时使用 `-i --no-healthcheck`，不要分配 TTY。

## 开发验收

运行 `go test -race -cover ./...`（覆盖率需超过 85%）、`go vet ./...` 和 `go build -o bin/images-mcp .`。测试使用本地模拟的中转和 S3，禁止发起真实的计费生成。
