[English](README_EN.md) | [简体中文](README.md)

# images-mcp

Generate and edit images through OpenAI-compatible relays, with local file or in-memory Base64 output. One Go binary connects to MCP clients over stdio.

> 🤖 **Want an AI Agent to install and configure it for you?**
> Send this prompt to your Agent:
> `Follow https://github.com/azicen/images-mcp/blob/main/AGENTS.md to install and configure images-mcp for me.`

## Why use it

- **Two API routes**: use Gemini Chat Completions and GPT Image Images APIs through one interface, with explicit per-model routing.
- **Unified multi-image editing**: `input_images` accepts local files, URLs, Data URIs, and base64. The first image is primary; subsequent images are references.
- **Layered timeouts**: request, model, and global settings override one another. Default to 10 minutes; allow 15 minutes for slower refinement.
- **One binary, flexible output**: no extra runtime dependencies. Results are downloaded or decoded automatically and returned as files, Base64, or both.

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
images-mcp -config /absolute/path/to/images-mcp.yaml
```

This is a stdio server launched by your MCP client. The client's `command` supports two forms:

- **Option A (recommended for simplicity)**: use `"images-mcp"`, provided its installation directory (usually `~/go/bin`) is in the client process's `PATH`. CLI clients such as Hermes, Claude Code, and Codex inherit this environment by default when launched from that shell. Check with `command -v images-mcp` (or `which images-mcp`).
- **Option B (compatibility fallback)**: use the binary's absolute path, such as `/absolute/path/to/images-mcp`. Use this when Go bin is not in `PATH`, or when a macOS GUI app (such as Claude Desktop) launched from Dock/Finder does not inherit the shell `PATH` containing `~/go/bin`.

**Never** use relative paths such as `./bin/images-mcp` that depend on the current working directory (CWD). **The `-config` argument must still be an absolute path**: the subprocess's startup CWD is uncontrolled, so a relative path may prevent it from finding the configuration file. Do not use `~` or unexpanded environment variables in client configuration paths.

Save the configuration and restart the client. See [the Agent installation guide](AGENTS.md#步骤-3注册-mcp-客户端) for JSON configurations for Claude Code, Cursor, Cline, Roo Code, and Hermes, and TOML for Codex.

## Recommended models

These five models come from the example configuration. Availability, pricing, and capabilities depend on your relay.

| Model | API | Suggested use |
| --- | --- | --- |
| `gemini-3.1-flash-image` | `chat` | Fast creation, text layout, multi-image fusion, character consistency |
| `gpt-image-2.5-flare` | `images` | Everyday generation, local edits, subject preservation |
| `gpt-image-2.5-sunburst` | `images` | Detailed final output and complex refinement; allow 15 minutes |
| `gpt-image-2.5` | `images` | General editing, natural lighting, transparent backgrounds |
| `gpt-image-2` | `images` | Batch drafts, exploration, thumbnails |

## Output configuration

```yaml
output_format: file # file | base64 | both
output_dir: ""
```

| Setting | Default | Behavior |
| --- | --- | --- |
| `output_format` | `file` | Global format, case insensitive and normalized to lowercase; invalid values fail startup |
| `output_dir` | `""` | File output directory; falls back to `IMAGE_OUTPUT_DIR`, then the OS temporary directory |

Priority: top-level `output_format` > `default.output_format` > `IMAGE_OUTPUT_FORMAT` > `file`. There are no request or model overrides.

| Mode | Behavior |
| --- | --- |
| `file` | Download or decode, save locally, return absolute `path`; omits Base64 to save context tokens |
| `base64` | Download or decode in memory and return `base64`; creates no directories or temporary files and uses no disk space |
| `both` | Save locally and return both `path` and `base64` |

Both `base64` and `both` include native MCP ImageContent, JSON text, and structured data for image rendering and plain text clients.

## MCP tools

| Tool | Purpose | Minimal arguments |
| --- | --- | --- |
| `list_models` | Discover configured models, guidance, and the default model | `{}` |
| `generate_image` | Generate or edit images using the global output format | `{"prompt":"A watercolor mountain landscape"}` |

`generate_image` also accepts `model`, `input_images`, and `timeout`. `size` and `quality` apply only to the Images route. Results include `model`, `route`, and each image's `mime_type`, `bytes`, and `path` and/or `base64` according to the global `output_format`.

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
