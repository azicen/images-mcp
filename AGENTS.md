# images-mcp：Agent 安装、配置与自愈

适用：Codex、Claude Code、Hermes、Cursor、Cline、Roo Code。按步骤执行；将 `<ABS_BIN>`、`<ABS_CONFIG_DIR>`、`<ABS_REPO>` 替换为已验证的实际绝对路径，不得原样写入客户端。

## 前置硬规则

1. **命令与配置路径**：客户端 `command` 优先使用可执行文件名 `"images-mcp"`，前提是其安装目录已加入客户端进程的 `PATH`；也可使用已验证的二进制绝对路径。严禁使用 `./bin/images-mcp` 等依赖当前工作目录（CWD）的相对路径。`-config` 参数仍必须使用绝对路径，禁止使用相对路径、`~` 或未展开的环境变量。stdio 子进程的 CWD 由宿主决定；相对路径可能导致找不到二进制或配置、秒退，并表现为 Agent 15s subprocess crash / 初始化超时。禁止依赖当前目录自动发现配置。
2. **凭据安全**：禁止虚构、猜测 API Key，禁止将占位值当作真实凭据发送。缺失时提示用户提供或自行配置中转 URL、API Key 和可用模型 ID；继续执行不依赖凭据的安装与本地验证。禁止输出密钥或提交含密钥的配置文件。
3. **显式协议**：所有配置模型（含自定义名称）必须声明 `api: chat` 或 `api: images`。禁止仅靠模型名前缀推断协议。
4. **配置合并**：保留宿主已有 MCP 配置，只新增或更新 `images-mcp` 条目。修改服务或客户端配置后重启宿主客户端。
5. **验证边界**：不得将 `list_models` 成功等同于中转凭据可用。仅在用户授权的生成任务中执行 `generate_image`，禁止自动重复可能计费的请求。

## 步骤 1：获取二进制

在 POSIX shell 执行；先验证工具链满足仓库 [go.mod](go.mod)：

```sh
go version
go install github.com/azicen/images-mcp@latest
echo "$(go env GOPATH)/bin/images-mcp"
```

解析安装目录；`GOBIN` 非空时优先使用，`GOPATH` 为列表时使用首项：

```sh
INSTALL_BIN_DIR="$(go env GOBIN)"
if [ -z "$INSTALL_BIN_DIR" ]; then
  INSTALL_GOPATH="$(go env GOPATH)"
  INSTALL_BIN_DIR="${INSTALL_GOPATH%%:*}/bin"
fi
ABS_BIN="$(cd "$INSTALL_BIN_DIR" && pwd -P)/images-mcp"
test -x "$ABS_BIN"
"$ABS_BIN" -h
```

要求：`-h` 退出码为 `0`。禁止将 Go 安装失败误报为安装成功。

备选源码构建；在选定的源码父目录执行（目标目录已存在时先检查，禁止覆盖）：

```sh
git clone https://github.com/azicen/images-mcp.git
cd images-mcp && go build -o bin/images-mcp .
ABS_REPO="$(pwd -P)"
ABS_BIN="$ABS_REPO/bin/images-mcp"
test -x "$ABS_BIN"
"$ABS_BIN" -h
```

## 步骤 2：生成配置文件

选择绝对配置目录；禁止覆盖已有配置。源码安装使用仓库模板；`go install` 安装使用下列下载分支（需要 curl）：

```sh
ABS_CONFIG_DIR="$HOME/.config/images-mcp"
umask 077
mkdir -p "$ABS_CONFIG_DIR"
ABS_CONFIG_DIR="$(cd "$ABS_CONFIG_DIR" && pwd -P)"
ABS_CONFIG="$ABS_CONFIG_DIR/images-mcp.yaml"
if [ ! -e "$ABS_CONFIG" ]; then
  if [ -n "${ABS_REPO:-}" ]; then
    cp "$ABS_REPO/images-mcp.example.yaml" "$ABS_CONFIG"
  else
    curl --fail --location https://raw.githubusercontent.com/azicen/images-mcp/main/images-mcp.example.yaml -o "$ABS_CONFIG"
  fi
fi
chmod 600 "$ABS_CONFIG"
```

将配置整理为以下最小完整结构；替换 `default` 的占位值后再调用生成。使用复制的模板时，删除不使用的 `families` 占位条目；家族凭据优先于 `default`，不得保留占位凭据遮蔽真实默认值。不同中转分别在对应模型中设置真实的 `base_url`、`api_key`。

```yaml
default_model: gemini-3.1-flash-image
timeout: 10m
output_dir: ""
output_format: file # file | base64 | both；全局生效
default:
  base_url: https://relay.example/v1
  api_key: REPLACE_WITH_USER_PROVIDED_API_KEY
models:
  gemini-3.1-flash-image:
    api: chat
    description: 快速创作、文字排版、多图融合与角色一致性。
  gpt-image-2.5-flare:
    api: images
    description: 日常生成、局部修改与主体保留。
  gpt-image-2.5-sunburst:
    api: images
    timeout: 15m
    description: 高精终稿与复杂精修。
  gpt-image-2.5:
    api: images
    description: 通用编辑、自然光影与透明背景。
  gpt-image-2:
    api: images
    description: 批量草稿与缩略图。
```

按用户中转实际支持的 ID 修改模型键与 `default_model`；禁止臆测模型可用性。`output_dir: ""` 按 `IMAGE_OUTPUT_DIR`、系统临时目录顺序回退；需持久保存时改为用户选定的绝对目录。

`output_format` 为全局配置，优先级：顶层 `output_format` > `default.output_format` > `IMAGE_OUTPUT_FORMAT` > `file`。仅允许 `file`、`base64`、`both`（大小写不敏感）；非法值会阻止启动。

按以下优先级定位配置覆盖，逐项独立解析 URL 与 Key：

```text
凭据：请求 > models.<id> > families.<family> > 家族环境变量 > default > 全局环境变量
超时：请求 timeout > models.<id>.timeout > default.timeout > 顶层 timeout > IMAGE_TIMEOUT > 10m
家族环境变量：GEMINI_BASE_URL / GEMINI_API_KEY；GPT_IMAGE_BASE_URL / GPT_IMAGE_API_KEY
全局环境变量：OPENAI_BASE_URL / OPENAI_API_KEY
```

禁止假设 YAML 支持 `${ENV_VAR}` 插值；使用真实配置值，或清空对应凭据字段后由宿主传入环境变量。超时使用正数 Go duration（`10m`、`15m`）或纯秒数字符串（`"600"`）。宿主更早的截止时间仍然有效。

## 步骤 3：注册 MCP 客户端

`command` 支持两种写法：

- **方式 A（优先推荐，简洁）**：使用 `"images-mcp"`。先将安装目录（通常为 `~/go/bin`，实际以步骤 1 为准）加入 `PATH`；Hermes、Claude Code、Codex 等从该 shell 启动的 CLI 客户端默认继承此环境，可直接使用命令名。可用 `command -v images-mcp`（或 `which images-mcp`）检查。
- **方式 B（兼容备选）**：使用步骤 1 验证的二进制绝对路径 `<ABS_BIN>`。适用于未将 Go bin 纳入 `PATH`，或 macOS GUI 应用（如 Claude Desktop）从 Dock/Finder 启动、未继承包含 `~/go/bin` 的 shell `PATH` 的场景。

严禁使用 `./bin/images-mcp` 等依赖 CWD 的相对路径。`-config` 参数始终必须使用绝对路径，因为子进程启动时的 CWD 不受控。将 `<ABS_CONFIG_DIR>` 替换为步骤 2 验证的实际绝对路径，不得在客户端中保留占位符、`~` 或未展开的环境变量。

以下示例使用方式 A。将 JSON 合并到 Claude Code、Cursor、Cline、Roo Code 或 Hermes 的 MCP 服务配置中；以宿主实际支持的导入入口为准：

```json
{
  "mcpServers": {
    "images-mcp": {
      "command": "images-mcp",
      "args": ["-config", "<ABS_CONFIG_DIR>/images-mcp.yaml"]
    }
  }
}
```

将以下条目合并到 Codex `config.toml`：

```toml
[mcp_servers.images-mcp]
command = "images-mcp"
args = ["-config", "<ABS_CONFIG_DIR>/images-mcp.yaml"]
```

使用方式 B 时，仅将上述 `command` 值替换为已验证的 `<ABS_BIN>` 实际绝对路径，`args` 保持绝对配置路径。

重启宿主客户端；重新加载 MCP 工具列表。

## 步骤 4：验证服务与工具连通

使用方式 A 时，先在启动客户端的环境中执行 `command -v images-mcp`（或 `which images-mcp`），确认命中预期二进制。再执行 `test -x "$ABS_BIN"`、`test -r "$ABS_CONFIG"`。使用 Python 3 进行有初始化握手的逐行 stdio JSON-RPC 验证；仅启动本地服务，不请求中转：

```sh
python3 - "$ABS_BIN" "$ABS_CONFIG" <<'PY'
import json
import queue
import subprocess
import sys
import threading

proc = subprocess.Popen(
    [sys.argv[1], "-config", sys.argv[2]],
    stdin=subprocess.PIPE, stdout=subprocess.PIPE, text=True,
)
messages = queue.Queue()

def read_stdout():
    for line in proc.stdout:
        messages.put(line)
    messages.put(None)

threading.Thread(target=read_stdout, daemon=True).start()

def send(message):
    proc.stdin.write(json.dumps({"jsonrpc": "2.0", **message}) + "\n")
    proc.stdin.flush()

def receive(request_id):
    while True:
        line = messages.get(timeout=15)
        if line is None:
            raise RuntimeError("Server closed stdout; inspect stderr and paths")
        message = json.loads(line)
        if message.get("id") == request_id:
            if "error" in message:
                raise RuntimeError(message["error"])
            return message["result"]

try:
    send({"id": 1, "method": "initialize", "params": {
        "protocolVersion": "2025-03-26", "capabilities": {},
        "clientInfo": {"name": "images-mcp-check", "version": "1.0"}}})
    result = receive(1)
    assert result["serverInfo"]["name"] == "images-mcp", result
    send({"method": "notifications/initialized"})
    send({"id": 2, "method": "tools/list", "params": {}})
    tools = receive(2)
    assert {"list_models", "generate_image"} <= {t["name"] for t in tools["tools"]}, tools
    send({"id": 3, "method": "tools/call", "params": {
        "name": "list_models", "arguments": {}}})
    result = receive(3)
    assert not result.get("isError"), result
    models = json.loads(next(c["text"] for c in result["content"] if c["type"] == "text"))
    assert models and any(m["is_default"] for m in models), models
    print(json.dumps({"status": "ok", "models": models}, ensure_ascii=False))
finally:
    proc.terminate()
    try:
        proc.wait(timeout=5)
    except subprocess.TimeoutExpired:
        proc.kill()
        proc.wait()
PY
```

要求：命令退出码 `0`、输出 `status: ok`。保留五款配置时要求返回五个模型；修改过配置时逐项核对实际模型与默认模型。失败时按步骤 5 修复，再执行本地验证。

在宿主调用 `list_models`，参数 `{}`；要求返回配置模型的 `id`、`description`、可选 `notes`、`supports_edit`、`is_default`，不得包含凭据。

在用户授权的生成任务中调用 `generate_image`：

```json
{"prompt":"水彩风格的山间风景","timeout":"15m"}
```

要求：`isError` 不为 `true`；结果包含 `model`、`route`、非空 `images`，每项包含 `mime_type`、正数 `bytes`；`file` 模式包含存在且可读取的绝对 `path`，`base64` 模式仅返回 `base64` 且不落盘，`both` 模式同时返回两者。含 Base64 的模式还返回原生 MCP 图像内容。未配置 `default_model` 时显式传入 `model`。编辑时添加有序 `input_images`；第一张为主图，其余为参考图。仅 Images 路由传入 `size`、`quality`。

## 步骤 5：排障与自愈矩阵

| Symptom | Cause | Fix |
| --- | --- | --- |
| 子进程秒退 / 15s 初始化超时 | 命令不在宿主 PATH 中；相对路径受宿主 CWD 影响；不可执行；配置不可读 | 在启动客户端的环境中执行 `command -v images-mcp`（或 `which images-mcp`）；若未找到，将安装目录加入 PATH，不想修改 PATH 时将 `command` 改为已验证的二进制绝对路径。执行 `test -x "$ABS_BIN"`、`test -r "$ABS_CONFIG"`、`"$ABS_BIN" -h`；仅对已确认的安装二进制执行 `chmod u+x "$ABS_BIN"`；确保 `-config` 为绝对路径，移除依赖 CWD 的相对路径并重启客户端。 |
| Unknown model family / unsupported model / 路由启动报错 | 自定义模型缺少显式协议或协议值无效 | 将对应 `models.<id>.api` 设为中转实际支持的 `chat` 或 `images`；移除冲突的 `api_type`；核对模型 ID；重启并调用 `list_models`。 |
| 慢速模型请求超时 | 模型或请求预算不足；宿主更早取消 | 将 `models.<id>.timeout` 或请求 `timeout` 设为 `15m`；核对宿主工具调用超时；配置修改后重启。 |
| 图片加载失败 | 输入格式或预算超限；文件不可读或 URL 不可访问 | 将输入转换为 PNG/JPEG/WebP；每张限制 32 MiB、每次合计 64 MiB、最多 16 张；修复报错中 `input_images[i]` 的路径或 URL；禁止只修改扩展名。 |
| 401 / 403 / 无效凭据 | Key 缺失、占位或被高优先级配置遮蔽 | 提示用户配置真实 Key；按步骤 2 的凭据优先级检查；移除未使用的家族占位配置；禁止猜测或打印 Key。 |
| `list_models` 返回空数组 | 未加载目标配置或 `models` 为空 | 核对 `-config` 绝对路径与文件内容；补齐 `models` 后重启；禁止将空数组解释为中转无模型。 |

修复后重复步骤 4 的本地验证。仅在用户任务需要且凭据已配置时调用生成；禁止自动更换模型或无限重试。

## HTTP 接入与 S3（远程 Agent）

默认 stdio 安装流程保持不变。需要远程接入时，在已验证的绝对配置文件中设置 `server.transport: http`、`server.listen`，或执行：

```sh
images-mcp -config /absolute/path/to/images-mcp.yaml -http 127.0.0.1:8080
```

`-http` 优先于配置 transport/listen；`-server-key` 优先于配置服务密钥。其余字段使用配置 > 环境回退：`MCP_TRANSPORT`、`MCP_LISTEN`、`MCP_SERVER_KEY` / `SERVER_API_KEY`。服务密钥与中转密钥不同，禁止互相混用、打印或提交。公开服务配置强密钥并通过 HTTPS 反向代理访问。

在宿主支持的远程 MCP 注册入口合并以下信息，保留已有条目：

- Streamable HTTP URL：`https://your-host/mcp`。
- SSE URL：`https://your-host/sse`，仅 `server.enable_sse: true` 时可用。
- 请求头：`Authorization: Bearer <用户配置的服务密钥>`；SSE 会话 POST 也必须传递。
- 兼容 `X-API-Key` 或 URL 查询 `?api_key=`；优先请求头，避免密钥进入访问日志。

各宿主的远程配置字段不同，不得将 stdio 的 command/args 当作远程 URL 配置。更新配置后重启服务、宿主或重新连接。`GET /healthz` 无需密钥，返回 200 `{"status":"ok"}`；缺失或错误密钥访问 `/mcp`、`/sse` 返回 JSON 401。未配置密钥时不会启用鉴权。

HTTP + `file` / `both` 必须配置 S3：非空 `s3.bucket`、完整 `s3.access_key_id` 和 `s3.secret_access_key`（或对应 AWS 环境变量），缺失或非法时启动前退出。禁止降级成本地路径。支持 MinIO/RustFS/R2/AWS，详细字段见 [配置模板](images-mcp.example.yaml)。桶应已存在且凭据允许 PutObject/GetObject/DeleteObject；自建服务指定 endpoint，R2 通常 region 为 auto。所有环境回退见 [README](README.md#http-服务与-s3-输出)。不得猜测存储凭据或将占位值用于真实请求。

HTTP 生成结果要求 `images[].url` 非空且无 `path`，URL 为配置的公开/CDN 前缀加对象键，或预签名 GET URL（默认 24h，最长 168h，临时凭据可能提前过期）。`both` 同时包含 Base64 和原生 MCP 图像；`base64` 无需 S3，也不会写磁盘。stdio 的 `file` / `both` 仍要求本地绝对 `path`。多图失败会尝试删除本次上传对象并报告删除错误。

先执行本地配置和探活验证，不自动请求中转或生成图片。`list_models` 成功不代表存储/中转权限有效；仅在用户授权生成任务中验证真实生图与上传，不重复可能计费的请求。客户端请求体上限为 96 MiB，宿主自身的更短超时仍有效。
