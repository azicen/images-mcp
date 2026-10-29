[English](README_EN.md) | [简体中文](README.md)

# images-mcp

A Go MCP server that generates and edits images through OpenAI-compatible relays and saves the results locally. It supports Gemini Chat Completions and GPT Image Images APIs, communicating with MCP clients over standard input/output (stdio). See [DESIGN.md](DESIGN.md) for routing, schema, and limits.

## Installation

A Go toolchain is required; the current [go.mod](go.mod) declares Go `1.27.1`. Model availability and supported parameters depend on your relay.

### Method A: go install

```sh
go install github.com/azicen/images-mcp@latest
```

By default, this installs the binary to `$GOPATH/bin/images-mcp` (usually `$HOME/go/bin/images-mcp` when GOPATH is unset). If `GOBIN` is set, Go installs it there instead. Use the actual absolute binary path in your client configuration, or add the installation directory to `PATH`.

### Method B: Build from source

```sh
git clone https://github.com/azicen/images-mcp.git
cd images-mcp
go build -o bin/images-mcp .
```

The binary is created at `bin/images-mcp` inside the repository. For development, run the local tests; they require no credentials and make no paid image generation requests:

```sh
go test -race -cover ./...
```

## Configure your MCP client

Copy [images-mcp.example.yaml](images-mcp.example.yaml) to `images-mcp.yaml`, then replace the relay URLs, API keys, and model IDs. Keep configuration files containing API keys private.

For clients using `mcpServers`, such as Claude Code and Cursor, use this stdio configuration. Replace the example paths with the actual absolute paths to the binary and configuration file:

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

Codex uses TOML in `config.toml`:

```toml
[mcp_servers.images-mcp]
command = "/absolute/path/to/images-mcp/bin/images-mcp"
args = ["-config", "/absolute/path/to/images-mcp.yaml"]
```

When using `go install`, set `command` to the absolute path of the installed binary. Other stdio-capable MCP clients can use the same command and arguments.

`-c` is an alias for `-config`. Without either flag, the server searches its **working directory** for `images-mcp.yaml`, `images-mcp.yml`, then `images-mcp.json`, loading the first existing file. An explicitly selected file must exist; an unreadable or invalid selected configuration stops startup. Configuration is read at startup, so restart after changes. Use `-h` for command-line help.

## Configuration reference (images-mcp.yaml)

```yaml
# images-mcp 配置文件
# 文档与说明参考: https://github.com/azicen/images-mcp

# 默认模型：当请求未传入 model 或为空时使用
default_model: gemini-3.1-flash-image

# 图像保存输出目录：留空时优先读取 IMAGE_OUTPUT_DIR 环境变量，未设置则使用系统临时目录
output_dir: ""

# 全局默认中转服务地址与 API Key（按需修改）
default:
  base_url: https://relay.example/v1
  api_key: your-default-api-key

# 精确模型配置与路由说明（会被 list_models 返回并用于智能体提示）
# api 可选:
#   - chat (Gemini 聊天补全路由, 支持文生图与基于 input_images 的多模态图生图)
#   - images (GPT Image 路由, 支持文生图与 multipart 图生图/编辑)
models:
  gemini-3.1-flash-image:
    api: chat
    description: 速度快成本低，擅长文字渲染、多图融合与角色一致。
    notes: 极速高性价比；画面排版与文字渲染精准；擅长多图融合与保持角色一致。

  gpt-image-2.5-flare:
    api: images
    description: 极速日常首选，生成快画质高，局部修改精准不走样。
    notes: 速度与画质兼备；局部修图（换衣服、改背景等）对原主体和构图保留最忠实。

  gpt-image-2.5-sunburst:
    api: images
    description: 细节与光影最强，适合高精终稿与复杂精修。
    notes: 微观细节与光影质感最强，多轮连续修改控制力最高；单次耗时稍长。

  gpt-image-2.5:
    api: images
    description: 标准通用款，光影自然，支持透明背景。
    notes: 质感自然无塑料感，支持透明背景与常规图文编辑。

  gpt-image-2:
    api: images
    description: 出图快成本低，适合大批量草稿与缩略图。
    notes: 成本低，适合初期灵感探索与大批量出草图。

families:
  gemini:
    base_url: https://gemini-relay.example/v1
    api_key: your-gemini-api-key
  gpt-image:
    base_url: https://gpt-image-relay.example/v1
    api_key: your-gpt-image-api-key
```

Replace the example model IDs with names supported by your relay.

| Field | Description |
| --- | --- |
| `default_model` | Used when a request omits `model` or supplies an empty string. If neither is available, the call returns an error. An explicit model takes precedence. |
| `output_dir` | Output directory. Priority: configuration file > `IMAGE_OUTPUT_DIR` > `os.TempDir()`. On Linux, the latter is usually `/tmp`, subject to the system temporary-directory settings. Empty file and environment values use this default. Relative paths resolve from the server's working directory; returned paths are absolute. |
| `default` | Global default `base_url` and `api_key`. |
| `models` | Routing, credentials, and discovery information keyed by **exact, case-sensitive model IDs**. Entries can override `base_url` and `api_key`. |
| `models.<id>.api` | `chat` or `images`, also accepting the aliases below. `api_type` is an alias; when both are set, they must select the same route. Required for custom names with unrecognized prefixes. |
| `models.<id>.description` | A short summary for agents, included in the `generate_image` tool description. |
| `models.<id>.notes` | Optional multiline guidance covering usage, limitations, and trade-offs, returned by `list_models`. |
| `families` | Fallback `base_url` and `api_key` for the `gemini` and `gpt-image` families. Explicit API selection determines the credential family. |

`description` and `notes` apply only to `models`, not `default` or `families`. Equivalent JSON configuration is supported with a `.json` extension. See [images-mcp.example.yaml](images-mcp.example.yaml) for the fully commented template.

## Dual-protocol routing

Route selection priority is: request `api` / `api_type` > exact model configuration `api` / `api_type` > case-insensitive model prefix. API values are also case-insensitive, and empty strings mean unspecified. Invalid values or conflicting aliases return errors; invalid model route settings prevent startup.

| Model family | API values | Endpoint | Behavior |
| --- | --- | --- | --- |
| Gemini | `chat`, `gemini` | `/v1/chat/completions` | Text-to-image generation; supplying nonempty `input_images` automatically builds a multimodal message containing text and an image data URI for image-to-image editing. |
| GPT Image | `images`, `gpt`, `gpt-image` | `/v1/images/generations` | Sends a JSON generation request when no nonempty input images are supplied. |
| GPT Image | Same as above | `/v1/images/edits` | Automatically sends a multipart image editing request when nonempty `input_images` are supplied. |

Without explicit API selection, the `gemini` and `gpt-image` prefixes select their respective families. Unlisted models can also be used through a recognized prefix or explicit API selection. If `base_url` does not end in `/v1`, the server appends it.

The Gemini route extracts images from chat response text (Markdown images, URLs, data URIs, or raw base64), content arrays, and `message.images`. The Images route reads `data[].b64_json` or `data[].url`. The server automatically downloads or decodes the results and saves them to the output directory.

Both routes accept local paths, HTTP(S) URLs, image Data URIs, and raw base64 through `input_images`. Inputs allow PNG, JPEG, and WebP only (GIF/SVG are rejected), up to 32 MiB each, 64 MiB total, and 16 images per request. Saved outputs allow PNG, JPEG, WebP, and GIF, with up to 16 extracted images per response. `size` and `quality` apply only to the Images route, with accepted values determined by your relay. Passing either parameter to the Chat route returns an error.

## Credential resolution priority

`base_url` and `api_key` resolve **independently**, in this order:

1. Request arguments: `base_url` / `api_key`.
2. Model configuration: `models.<exact model ID>`.
3. Family configuration: `families.gemini` / `families.gpt-image`.
4. Family environment variables: `GEMINI_BASE_URL` / `GEMINI_API_KEY`, or `GPT_IMAGE_BASE_URL` / `GPT_IMAGE_API_KEY`.
5. Global configuration defaults: `default`.
6. Global environment variables: `OPENAI_BASE_URL` / `OPENAI_API_KEY`.

Empty or whitespace-only credential fields fall through. Overriding one field does not change the source of the other. An invalid nonempty winning value returns an error without further fallback. `base_url` must be an HTTP(S) URL without embedded credentials, query parameters, or fragments; `api_key` must be a nonempty printable ASCII token without whitespace.

## Exposed MCP tools

### generate_image

Generates or edits images through a unified interface. The SDK automatically derives input/output schemas from the Go types; the tool description dynamically includes the configured default model and model summaries sorted by ID at startup. The schema's parameter set does not change with the model catalog.

| Argument | Description |
| --- | --- |
| `prompt` | Required, nonblank generation or editing instructions. |
| `model` | Optional; omitted or empty values use `default_model`. |
| `input_images` | Optional ordered image array; blank entries are ignored. First image is primary; remaining images are style/context references. |
| `api` / `api_type` | Optional protocol route override for the model configuration. |
| `base_url` / `api_key` | Optional credential overrides for this request. |
| `size` / `quality` | Optional; supported only on the Images route. |

Use the default model:

```json
{"prompt":"A watercolor mountain landscape"}
```

Explicitly select a model and route for editing:

```json
{"model":"custom-image-model","api":"images","prompt":"Make the sky golden","input_images":["/absolute/path/to/input.png"],"size":"1024x1024"}
```

Successful results return `model`, `route`, and `images` in both JSON text and structured MCP content. Each image includes its absolute `path`, `mime_type`, and `bytes`. The returned `route` is the endpoint suffix without `/v1`, such as `/images/edits`.

**Error recovery support:** missing-model and unroutable-model errors list configured model IDs and suggest calling `list_models`, allowing an agent to correct the arguments and call again. Failures are returned as MCP tool errors; the server does not automatically retry requests or switch models. Stdout carries only MCP traffic.

### list_models

Call with `{}` to discover configured models and retrieve detailed guidance. Returns a JSON array sorted by ID, with each entry containing:

- `id`: exact model ID.
- `description`: short summary.
- `notes`: optional detailed guidance.
- `supports_edit`: editing support; `true` for both routes.
- `is_default`: whether this is the configured default model.

Only entries in `models` are listed. The tool does not query the relay or return credentials. Model summaries appear in the generation tool description; call this tool for complete notes.

## Optional: Environment-only fallback

Environment variables can supplement a configuration file or be used on their own. To run using only environment variables, omit `-config` and ensure no automatically discovered configuration file exists in the server's working directory:

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

Set family variables as needed; omitted family credentials fall back to global credentials. Without a configuration file, `list_models` returns an empty array, and the default model, model summaries, and detailed notes are unavailable. Supply `model` explicitly on every call; custom model names also require `api` or `api_type`.


### Multi-image references and image-to-image editing

```json
{"model":"gpt-image-2.5-flare","prompt":"Keep the subject in image 1, use the palette of image 2 and the setting of image 3","input_images":["/absolute/path/to/primary.png","https://example.com/style.webp","data:image/jpeg;base64,..."]}
```

Gemini models accept the same arguments. Supply a one-element `input_images` array for single-image editing. Blank entries are ignored; order and duplicates are preserved. The first image is the primary image to edit; subsequent images provide style/context references. Describe their intended roles in the prompt; the model determines the final result.

With inputs, GPT Image uses `/v1/images/edits`: one file uses `image`; multiple files each use `image[]`, with individual filenames and MIME headers. Gemini uses `/v1/chat/completions` with a user content array containing the text part first, then ordered `image_url` parts encoded as base64 Data URIs. With no nonblank input, text-to-image behavior applies.

More than 16 inputs fail before any image download or generation request. Exceeding the cumulative 64 MiB budget stops loading immediately. Loading and budget errors identify the original argument, such as `input_images[2]` (zero-based array indices).
