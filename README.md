[English](README_EN.md) | [简体中文](README.md)

# images-mcp

使用 Go 编写的 MCP 服务，通过 OpenAI 兼容中转服务统一完成图像生成与编辑，并将结果保存到本地。支持 Gemini Chat Completions 和 GPT Image Images 两种协议，通过标准输入输出（stdio）与 MCP 客户端通信。路由、Schema 和限制的设计说明见 [DESIGN.md](DESIGN.md)。

## 安装与构建 (Installation)

需要 Go 工具链；当前 [go.mod](go.mod) 声明的 Go 版本为 `1.27.1`。模型和参数是否可用取决于所使用的中转服务。

### 方法 A：使用 go install

```sh
go install github.com/azicen/images-mcp@latest
```

默认将可执行文件安装到 `$GOPATH/bin/images-mcp`（未设置 GOPATH 时通常是 `$HOME/go/bin/images-mcp`）。如果设置了 `GOBIN`，则安装到该目录。客户端配置中请使用实际的绝对路径，也可以将安装目录加入 `PATH`。

### 方法 B：从源码构建

```sh
git clone https://github.com/azicen/images-mcp.git
cd images-mcp
go build -o bin/images-mcp .
```

构建结果为仓库内的 `bin/images-mcp`。开发时可运行本地测试，无需凭据，也不会发起付费图像生成请求：

```sh
go test -race -cover ./...
```

## 配置 MCP 客户端

先复制 [images-mcp.example.yaml](images-mcp.example.yaml) 为 `images-mcp.yaml`，替换其中的中转地址、API 密钥和模型 ID。包含密钥的配置文件应妥善保管。

Claude Code、Cursor 等使用 `mcpServers` 配置的客户端，可采用以下 stdio 配置。将示例路径替换为可执行文件和配置文件的实际绝对路径：

```json
{
  "mcpServers": {
    "images-mcp": {
      "command": "/absolute/path/to/images-mcp/bin/images-mcp",
      "args": ["-config", "/absolute/path/to/images-mcp.yaml"]
    }
  }
}
```

Codex 的 `config.toml` 使用 TOML 格式：

```toml
[mcp_servers.images-mcp]
command = "/absolute/path/to/images-mcp/bin/images-mcp"
args = ["-config", "/absolute/path/to/images-mcp.yaml"]
```

使用 `go install` 时，将 `command` 改为安装后的二进制绝对路径。其他支持 stdio 的 MCP 客户端同样配置 `command` 和 `args` 即可。

`-config` 可简写为 `-c`。未指定时，服务按顺序查找**工作目录**中的 `images-mcp.yaml`、`images-mcp.yml`、`images-mcp.json`，加载第一个存在的文件。显式指定的文件必须存在；选中的配置文件无法读取或格式无效时，启动失败。配置在启动时读取，修改后需重启。使用 `-h` 查看命令行帮助。

## 配置文件详解 (images-mcp.yaml)

```yaml
default_model: gemini-3.1-flash-image
output_dir: ""

default:
  base_url: https://relay.example/v1
  api_key: your-default-api-key

models:
  gemini-3.1-flash-image:
    api: chat
    description: 通过 Gemini 聊天接口生成和编辑图像。
    notes: |
      编辑时传入 input_image。
      此路由不接受 size 或 quality。
  custom-image-model:
    api: images
    description: 使用 Images API 的自定义图像模型。
    notes: |
      传入 input_image 时使用 multipart 图像编辑接口。
      size 和 quality 的可用值取决于中转服务。
    base_url: https://custom-relay.example/v1
    api_key: your-model-api-key

families:
  gemini:
    base_url: https://gemini-relay.example/v1
    api_key: your-gemini-api-key
  gpt-image:
    base_url: https://gpt-image-relay.example/v1
    api_key: your-gpt-image-api-key
```

示例模型 ID 需替换为中转服务实际支持的名称。

| 字段 | 说明 |
| --- | --- |
| `default_model` | 请求未传 `model` 或传入空字符串时使用。未配置且请求也未指定模型时返回错误。显式模型参数优先。 |
| `output_dir` | 输出目录。优先级为配置文件 > `IMAGE_OUTPUT_DIR` > `os.TempDir()`；Linux 通常为 `/tmp`，实际由系统临时目录设置决定。配置和环境变量均为空时使用该默认值。相对路径基于服务工作目录解析，最终返回绝对路径。 |
| `default` | 全局默认 `base_url` 和 `api_key`。 |
| `models` | 按**区分大小写的精确模型 ID**配置路由、凭据及发现信息。可覆盖 `base_url`、`api_key`。 |
| `models.<id>.api` | `chat` 或 `images`，也接受下文所列别名。`api_type` 是同义字段；同时设置时必须指向相同路由。自定义名称无法由前缀识别时必须配置。 |
| `models.<id>.description` | 面向智能体的简短说明，会加入 `generate_image` 工具描述。 |
| `models.<id>.notes` | 可选多行详细指南，用于记录参数限制、使用方式和取舍，由 `list_models` 返回。 |
| `families` | `gemini` 和 `gpt-image` 两个家族的 `base_url`、`api_key` 回退配置。显式 API 路由决定凭据家族。 |

`description` 和 `notes` 仅用于 `models`，不用于 `default` 或 `families`。也支持扩展名为 `.json` 的等价 JSON 配置；完整注释模板见 [images-mcp.example.yaml](images-mcp.example.yaml)。

## 双路协议路由机制

路由选择优先级为：请求的 `api` / `api_type` > 精确模型配置中的 `api` / `api_type` > 模型名前缀（不区分大小写）。API 值也不区分大小写，空字符串视为未指定；无效值或互相冲突的别名会返回错误，模型配置中的无效路由会阻止启动。

| 模型家族 | API 值 | 端点 | 行为 |
| --- | --- | --- | --- |
| Gemini | `chat`、`gemini` | `/v1/chat/completions` | 文生图；提供 `input_image` 时自动构造含文本和图像 data URI 的多模态消息，实现图生图。 |
| GPT Image | `images`、`gpt`、`gpt-image` | `/v1/images/generations` | 未提供 `input_image` 时发送 JSON 生成请求。 |
| GPT Image | 同上 | `/v1/images/edits` | 提供 `input_image` 时自动发送 multipart 图像编辑请求。 |

未显式指定路由时，`gemini` 和 `gpt-image` 前缀分别选择对应家族。未列入配置的模型也可通过前缀或显式 API 使用。`base_url` 末尾没有 `/v1` 时会自动补充。

Gemini 路由会从聊天响应文本（Markdown 图像、URL、data URI 或纯 base64）、内容数组及 `message.images` 中提取图像；Images 路由解析 `data[].b64_json` 或 `data[].url`。服务自动下载或解码结果并保存到输出目录。

两种路由的 `input_image` 均支持本地路径、HTTP(S) URL、图像 data URI 和原始 base64。支持 PNG、JPEG、GIF、WebP，单张图像上限为 32 MiB，每次响应最多提取 16 张图像。`size` 和 `quality` 仅适用于 Images 路由，可用值由中转服务决定；Chat 路由传入这两个参数会返回错误。

## 凭据解析优先级

`base_url` 和 `api_key` **分别独立**按以下优先级解析：

1. 请求参数：`base_url` / `api_key`。
2. 配置模型：`models.<精确模型 ID>`。
3. 配置家族：`families.gemini` / `families.gpt-image`。
4. 家族环境变量：`GEMINI_BASE_URL` / `GEMINI_API_KEY` 或 `GPT_IMAGE_BASE_URL` / `GPT_IMAGE_API_KEY`。
5. 配置全局默认：`default`。
6. 全局环境变量：`OPENAI_BASE_URL` / `OPENAI_API_KEY`。

空值或仅含空白的凭据字段会继续向下查找；仅覆盖一个字段不会改变另一个字段的来源。最终选中的非空值若无效则返回错误，不再继续回退。`base_url` 必须是无内嵌凭据、查询参数或片段的 HTTP(S) URL；`api_key` 必须是非空、无空白的可打印 ASCII token。

## 暴露的 MCP 工具

### generate_image

统一生成或编辑图像。SDK 根据 Go 参数类型自动生成输入/输出 Schema；工具描述则在启动时动态加入配置的默认模型和按 ID 排序的模型简介。Schema 的参数集合不会随模型目录变化。

| 参数 | 说明 |
| --- | --- |
| `prompt` | 必填，非空的生成或编辑指令。 |
| `model` | 可选，省略或为空时使用 `default_model`。 |
| `input_image` | 可选，提供时执行图像编辑。 |
| `api` / `api_type` | 可选，覆盖模型配置的协议路由。 |
| `base_url` / `api_key` | 可选，覆盖本次请求的凭据。 |
| `size` / `quality` | 可选，仅 Images 路由支持。 |

使用默认模型：

```json
{"prompt":"水彩风格的山间风景"}
```

显式选择模型和路由进行编辑：

```json
{"model":"custom-image-model","api":"images","prompt":"将天空改成金色","input_image":"/absolute/path/to/input.png","size":"1024x1024"}
```

成功结果通过 JSON 文本和 MCP 结构化内容返回 `model`、`route` 和 `images`；每张图像包含绝对 `path`、`mime_type` 和 `bytes`。`route` 返回不含 `/v1` 的端点后缀，如 `/images/edits`。

**错误自愈支持**：缺少模型或模型无法路由时，错误会列出配置的模型 ID，并提示调用 `list_models`，供智能体据此修正参数后重新调用。服务将失败作为 MCP 工具错误返回，不会自动重试或自动更换模型。标准输出仅承载 MCP 通信。

### list_models

使用 `{}` 调用，发现配置模型并获取详细指南。返回按 ID 排序的 JSON 数组，每条包含：

- `id`：精确模型 ID。
- `api`：规范化路由，`chat` 或 `images`。
- `description`：简短说明。
- `notes`：可选详细指南。
- `supports_edit`：是否支持编辑，两条路由均为 `true`。
- `is_default`：是否为配置的默认模型。

仅列出 `models` 中的条目，不向中转服务查询模型，也不会返回凭据。工具描述包含模型简介；需要完整注意事项时应调用本工具。

## 可选：纯环境变量运行 (Fallback)

环境变量既可作为配置文件的回退，也可单独使用。纯环境变量运行时，省略 `-config`，并确保服务工作目录中不存在自动发现的配置文件：

```json
{
  "mcpServers": {
    "images-mcp": {
      "command": "/absolute/path/to/images-mcp/bin/images-mcp",
      "env": {
        "OPENAI_BASE_URL": "https://relay.example/v1",
        "OPENAI_API_KEY": "your-default-key",
        "GEMINI_BASE_URL": "https://gemini-relay.example/v1",
        "GEMINI_API_KEY": "your-gemini-key",
        "GPT_IMAGE_BASE_URL": "https://gpt-image-relay.example/v1",
        "GPT_IMAGE_API_KEY": "your-gpt-image-key",
        "IMAGE_OUTPUT_DIR": "/absolute/path/to/generated-images"
      }
    }
  }
}
```

按需设置家族变量；不设置时会使用全局凭据。没有配置文件时，`list_models` 返回空数组，默认模型、模型简介和详细指南不可用。每次调用需显式传入 `model`；自定义模型名称还需传入 `api` 或 `api_type`。
