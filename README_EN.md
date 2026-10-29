[English](README_EN.md) | [简体中文](README.md)

# images-mcp

Generate and edit images through OpenAI-compatible relays, with results saved locally. One Go binary connects to MCP clients over stdio.

> 🤖 **Want an AI Agent to install and configure it for you?**
> Send this prompt to your Agent:
> `Follow https://github.com/azicen/images-mcp/blob/main/AGENTS.md to install and configure images-mcp for me.`

## Why use it

- **Two API routes**: use Gemini Chat Completions and GPT Image Images APIs through one interface, with explicit per-model routing.
- **Unified multi-image editing**: `input_images` accepts local files, URLs, Data URIs, and base64. The first image is primary; subsequent images are references.
- **Layered timeouts**: request, model, and global settings override one another. Default to 10 minutes; allow 15 minutes for slower refinement.
- **One binary, local output**: no extra runtime dependencies. Results are downloaded or decoded automatically and returned as absolute file paths.

## Quick start

### 1. Install

Use a Go toolchain satisfying [go.mod](go.mod):

```sh
go install github.com/azicen/images-mcp@latest
```

### 2. Configure

Copy [images-mcp.example.yaml](images-mcp.example.yaml) to your configuration directory. Enter your real relay URL, API key, and supported model IDs. Explicitly set `api: chat` or `api: images` for every model. Keep files containing credentials private.

### 3. Run

```sh
/absolute/path/to/images-mcp -config /absolute/path/to/images-mcp.yaml
```

This is a stdio server launched by your MCP client. Register the executable and arguments using **absolute paths for both files**, then restart the client. See [the Agent installation guide](AGENTS.md#步骤-3注册-mcp-客户端) for JSON configurations for Claude Code, Cursor, Cline, Roo Code, and Hermes, and TOML for Codex.

## Recommended models

These five models come from the example configuration. Availability, pricing, and capabilities depend on your relay.

| Model | API | Suggested use |
| --- | --- | --- |
| `gemini-3.1-flash-image` | `chat` | Fast creation, text layout, multi-image fusion, character consistency |
| `gpt-image-2.5-flare` | `images` | Everyday generation, local edits, subject preservation |
| `gpt-image-2.5-sunburst` | `images` | Detailed final output and complex refinement; allow 15 minutes |
| `gpt-image-2.5` | `images` | General editing, natural lighting, transparent backgrounds |
| `gpt-image-2` | `images` | Batch drafts, exploration, thumbnails |

## MCP tools

| Tool | Purpose | Minimal arguments |
| --- | --- | --- |
| `list_models` | Discover configured models, guidance, and the default model | `{}` |
| `generate_image` | Generate or edit images and save them locally | `{"prompt":"A watercolor mountain landscape"}` |

`generate_image` also accepts `model`, `input_images`, and `timeout`. `size` and `quality` apply only to the Images route. Results include `model`, `route`, and each image's absolute `path`, `mime_type`, and `bytes`.

Inputs must be PNG/JPEG/WebP: at most 16 images, 32 MiB each, and 64 MiB total. `list_models` reads configured model entries; it does not query the relay.

## Documentation

| Document | Contents |
| --- | --- |
| [AGENTS.md](AGENTS.md) | Automated installation, minimal complete configuration, client registration, verification, and recovery |
| [images-mcp.example.yaml](images-mcp.example.yaml) | Complete commented configuration template and model guidance |
| [DESIGN.md](DESIGN.md) | Routing, schema, credential and timeout resolution, and image limits |

Validate and build for development:

```sh
go test -race -cover ./...
go build -o bin/images-mcp .
```
