# images-mcp

[English](README_EN.md) | [简体中文](README.md)

让 Claude Code、Cursor、Codex 这类 AI Agent 在对话里直接画图、改图。

ChatGPT 和 Gemini 的网页版可以在聊天里出图，但在 IDE 或终端里用 Claude Code、Claude Desktop、Cursor、Codex 时，Agent 本身画不了图。要配图、换图标、画示意图，只能切到浏览器生成，再下载拖回项目。

images-mcp 通过 OpenAI 兼容的中转接入 Gemini 和 GPT 图片模型，这些事在当前对话里就能做完。改图时给出本地图片路径即可，程序会自己读取文件，不用先传到图床。

> 🤖 想让 Agent 帮你安装配置？把这句话发给它：
>
> `按照 https://github.com/azicen/images-mcp/blob/main/AGENTS.md 为我安装和配置 images-mcp`

## 两种用法

- **本地直连（standalone，默认）**：适合个人使用。本机进程直接请求中转，图片保存到本地；也可以设为 `base64`，图片只在内存中返回给客户端显示，不写文件。
- **远程服务 + 本地代理（server + forward）**：适合团队或多台机器。远端 server 统一保管中转 Key，输出文件时把图片存进 S3；Agent 所在机器运行轻量的 forward，由它读取本地参考图并流式发给远端。

## 快速上手

### 1. 准备中转并安装

先准备好 OpenAI 兼容中转的 Base URL、API Key，以及中转实际提供的图片模型 ID。

推荐从 [GitHub Releases](https://github.com/azicen/images-mcp/releases) 下载预编译的静态二进制，提供 Windows、Linux、macOS 的 amd64 / arm64 版本。压缩包里的文件名带平台后缀（如 `images-mcp-linux-amd64`），解压后改名为 `images-mcp`（Windows 为 `images-mcp.exe`），放进 PATH，再运行 `images-mcp -h` 确认可用。

也可以用 Go 安装，或在源码根目录自行构建。工具链版本以 [go.mod](go.mod) 为准（当前为 Go 1.27.1）：

```sh
go install github.com/azicen/images-mcp@latest
```

```sh
go build -o bin/images-mcp .
```

### 2. 写最小配置

新建 `images-mcp.yaml`。把下面的模型 ID 换成你的中转实际支持的模型，`default_model` 也要跟着改。Gemini 用 `api: chat`，GPT 图片模型用 `api: images`。示例里的模型名只是示范，不代表你的中转一定提供。

```yaml
mode: standalone
output_format: file
default_model: gemini-3.1-flash-image
timeout: 10m
default:
  base_url: "" # 留空时读取 OPENAI_BASE_URL
  api_key: "" # 留空时读取 OPENAI_API_KEY
models:
  gemini-3.1-flash-image:
    api: chat
    description: 图片生成与编辑
    tags: [text-rendering, multi-image]
```

在启动 Agent 客户端的环境里设置 `OPENAI_BASE_URL` 和 `OPENAI_API_KEY`。也可以直接把实际值写进配置文件，但要收紧文件权限，且不要提交到仓库。全部选项见 [images-mcp.example.yaml](images-mcp.example.yaml)。

图片默认写入系统临时目录，返回绝对路径；用 `output_dir` 可以指定保存位置。`output_format` 设为 `base64` 时直接返回图片内容、不写文件，设为 `both` 则两者都返回。

### 3. 接入 Agent 客户端

Claude Desktop（`claude_desktop_config.json`）、Cursor（`mcp.json`）和 Claude Code（项目里的 `.mcp.json`）都用下面这种 JSON 结构，把条目合并进已有的 `mcpServers`：

```json
{
  "mcpServers": {
    "images-mcp": {
      "command": "images-mcp",
      "args": ["-config", "/absolute/path/images-mcp.yaml"]
    }
  }
}
```

Claude Code 也可以用命令添加：

```sh
claude mcp add images-mcp -- images-mcp -config /absolute/path/images-mcp.yaml
```

Codex 在 `~/.codex/config.toml` 中添加：

```toml
[mcp_servers.images-mcp]
command = "images-mcp"
args = ["-config", "/absolute/path/images-mcp.yaml"]
```

配置路径要换成实际的绝对路径，不能用 `~`。`command` 填客户端 PATH 里能找到的 `images-mcp`；GUI 客户端找不到命令时，改填二进制的绝对路径。其他 MCP 条目保持不动，保存后重启客户端。

### 4. 验证连接

重启后应能看到 `generate_image` 和 `list_models` 两个工具。让 Agent 调用一次 `list_models`，核对模型列表和默认模型是否与配置一致。这一步只说明配置已加载、工具能调通，不代表中转能正常出图，也不检查 S3 权限。

之后就可以直接说“给这个项目画个图标”，或者给出一张本地图片的绝对路径，说“保留主体，把背景换成山景”。

## MCP 工具

### generate_image

支持文生图和多图编辑。下面的参数示例都使用配置里的默认模型。

文生图：

```json
{"prompt":"为天气应用画一个简洁的太阳图标，透明背景"}
```

多图参考，第一张是要修改的主图，其余作为风格或内容参考：

```json
{
  "prompt": "保留第一张图的主体，采用第二张图的配色与画风",
  "input_images": ["/absolute/path/subject.png", "/absolute/path/style.jpg"],
  "timeout": "15m"
}
```

| 参数 | 说明 |
| --- | --- |
| `prompt` | 必填，描述要生成或修改的内容 |
| `model` | 可选，指定已配置的模型；没有设置默认模型时必填 |
| `input_images` | 可选，有序参考图列表，本地文件用绝对路径，也可以是 HTTP(S) URL；最多 16 张，仅限 PNG / JPEG / WebP |
| `quality` | 可选，Images 模型的质量档位，可选值看模型配置 |
| `size` | 可选，Images 模型的预设或自定义尺寸，如 `1024x1024` |
| `timeout` | 可选，本次请求的超时，如 `15m` |

不传 `quality` / `size` 时使用模型配置的默认值。模型开启 `supports_custom_size` 后，自定义宽高会对齐到最近的 16 的倍数，例如 `1920x1080` 变成 `1920x1088`；对齐后单边不超过 3840px，长宽比在 1:3 到 3:1 之间，总像素在 655,360 到 8,294,400 之间。格式不对、尺寸超限，或模型不允许的尺寸都会直接报错。质量档位不受支持时，按配置回退到默认档位。`api: chat` 模型（如 Gemini）会忽略 `quality` 和 `size`。这些调整都会写在结果的 `notice` 字段里。

输出为 `file` 或 `both` 时，本地直连返回文件路径，远程服务返回图片 URL；输出含 Base64 时，结果里还会附带 MCP 原生图片内容。生成失败不会自动重试，也不会换模型。

### list_models

无需参数。返回已配置模型的 ID、描述、默认标记和编辑能力，以及可选的 `notes`、`tags`、质量档位与默认质量、预设尺寸与默认尺寸、是否支持自定义尺寸，供 Agent 挑选模型和尺寸。列表直接取自配置，不会去中转查询哪些模型可用。

## 远程部署与 Docker

远端使用 `mode: server`，中转和模型配置照旧，另外用 `MCP_SERVER_KEY` 设置服务密钥（与中转 Key 是两回事）。输出为 `file` 或 `both` 时需要一个已存在的 S3 桶和访问凭据，MinIO、RustFS、R2、AWS 等兼容服务都可以；纯 `base64` 输出不需要 S3。具体字段见配置模板里的 `server` 和 `s3` 部分。

准备好配置文件和环境变量后启动容器。示例里的主机路径要换成实际路径，且配置文件必须能被容器用户（UID 10001）读取：

```sh
docker run -d --name images-mcp --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  --mount type=bind,src=/etc/images-mcp/config.yaml,dst=/etc/images-mcp/config.yaml,readonly \
  -e OPENAI_BASE_URL -e OPENAI_API_KEY -e MCP_SERVER_KEY \
  -e S3_BUCKET -e S3_ENDPOINT -e S3_REGION -e S3_USE_PATH_STYLE \
  -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY \
  ghcr.io/azicen/images-mcp:latest \
  -config /etc/images-mcp/config.yaml -mode server -listen :8080
```

生产环境请固定镜像版本或摘要，并通过 HTTPS 反向代理对外提供服务。探活用 `GET /healthz`。

本地 Agent 的 MCP 条目仍是 `command` / `args` 结构，只需把 `args` 换成：

```json
["-mode", "forward", "-forward-url", "https://images.example.com"]
```

示例 URL 换成实际的服务地址，再在客户端环境里用 `IMAGES_MCP_FORWARD_KEY` 传入与服务端相同的服务密钥。本地 forward 不需要中转 Key 或 S3，改图时照样传本地图片路径。server 只提供 REST API，不能直接注册成 MCP，Agent 始终通过本地 forward 以 stdio 方式接入。

## 更多文档

- [AGENTS.md](AGENTS.md)：供 Agent 使用的安装、验证与排障步骤
- [DESIGN.md](DESIGN.md)：架构设计
- [images-mcp.example.yaml](images-mcp.example.yaml)：完整配置模板

## 开发验证

```sh
go test -race -cover ./...
go vet ./...
go build -o bin/images-mcp .
```

测试使用本地模拟的中转和 S3，不会调用真实的计费服务。
