[English](README_EN.md) | [简体中文](README.md)

# images-mcp

通过 OpenAI 兼容中转服务统一生成、编辑图片，并自动保存到本地。一个 Go 二进制，通过 stdio 接入 MCP 客户端。

> 🤖 **让 AI Agent 帮你自动安装与配置？**
> 直接将这句提示词发给你的 Agent：
> `按照 https://github.com/azicen/images-mcp/blob/main/AGENTS.md 为我安装和配置 images-mcp`

## 为什么用它

- **双路路由**：统一接入 Gemini Chat Completions 与 GPT Image Images API，按模型配置选择协议。
- **统一多图编辑**：同一个 `input_images` 参数支持本地文件、URL、Data URI 和 base64；第一张为主图，后续为参考图。
- **分层可配置超时**：请求、模型及全局超时逐层覆盖，默认 10 分钟，慢速精修可设为 15 分钟。
- **单二进制本地落盘**：运行时无需额外依赖，自动下载或解码结果，返回本地绝对路径。

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
/absolute/path/to/images-mcp -config /absolute/path/to/images-mcp.yaml
```

这是 stdio 服务，由 MCP 客户端启动并通信。将上述二进制与参数注册到客户端，**两个路径均须为绝对路径**，然后重启客户端。Claude Code、Cursor、Cline、Roo Code、Hermes 的 JSON 和 Codex 的 TOML 配置见 [Agent 安装指南](AGENTS.md#步骤-3注册-mcp-客户端)。

## 推荐模型

以下为配置示例中的五款模型；实际可用性、价格和能力以中转服务为准。

| 模型 | 协议 | 推荐用途 |
| --- | --- | --- |
| `gemini-3.1-flash-image` | `chat` | 快速创作、文字排版、多图融合与角色一致性 |
| `gpt-image-2.5-flare` | `images` | 日常生成、局部修改与主体保留 |
| `gpt-image-2.5-sunburst` | `images` | 高精终稿、复杂精修；建议超时 15 分钟 |
| `gpt-image-2.5` | `images` | 通用图文编辑、自然光影与透明背景 |
| `gpt-image-2` | `images` | 批量草稿、灵感探索与缩略图 |

## MCP 工具

| 工具 | 用途 | 最小参数 |
| --- | --- | --- |
| `list_models` | 查看已配置模型、用途及默认模型 | `{}` |
| `generate_image` | 生成或编辑图片并保存到本地 | `{"prompt":"水彩风格的山间风景"}` |

`generate_image` 可传 `model`、`input_images` 和 `timeout`；`size`、`quality` 仅适用于 Images 路由。结果包含 `model`、`route`，以及每张图片的绝对 `path`、`mime_type` 和 `bytes`。

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
