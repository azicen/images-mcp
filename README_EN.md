# images-mcp

[English](README_EN.md) | [简体中文](README.md)

Let Claude Code, Cursor, Codex, and other AI agents generate and edit images right in the conversation.

ChatGPT and Gemini can make images in their web apps, but when you work in Claude Code, Claude Desktop, Cursor, or Codex, the agent itself can't. Need an illustration, a new icon, or a diagram? You switch to a browser, generate it there, and download it back into your project.

images-mcp connects your agent to Gemini and GPT image models through an OpenAI-compatible relay, so all of that happens in the conversation you're already in. To edit an image, just give its local path. images-mcp reads the file itself, so there's nothing to upload to an image host first.

> 🤖 Want your agent to set it up for you? Send it this prompt:
>
> `Follow https://github.com/azicen/images-mcp/blob/main/AGENTS.md to install and configure images-mcp for me.`

## Two ways to run it

- **Standalone (default)**: best for personal use. A local process calls your relay and saves images to disk. Set the output to `base64` to return images in memory for the client to display, without writing files.
- **Remote server + local forward (server + forward)**: for teams or multiple machines. The remote server holds the relay keys and, when outputting files, stores images in S3. A lightweight forward process on the agent's machine reads local reference images and streams them to the server.

## Quick start

### 1. Get relay access and install

You need the Base URL and API key of an OpenAI-compatible relay, plus the ID of an image model the relay actually serves.

The easiest route is a prebuilt static binary from [GitHub Releases](https://github.com/azicen/images-mcp/releases), available for Windows, Linux, and macOS on amd64 and arm64. Archive contents carry a platform suffix (for example `images-mcp-linux-amd64`). After extracting, rename the binary to `images-mcp` (`images-mcp.exe` on Windows), put it on your PATH, and run `images-mcp -h` to confirm it works.

You can also install with Go or build from the repository root. The required toolchain is listed in [go.mod](go.mod) (currently Go 1.27.1):

```sh
go install github.com/azicen/images-mcp@latest
```

```sh
go build -o bin/images-mcp .
```

### 2. Write a minimal config

Create `images-mcp.yaml`. Replace the model ID below with one your relay supports, and change `default_model` to match. Use `api: chat` for Gemini and `api: images` for GPT image models. The model name here is only an example; it doesn't mean your relay offers it.

```yaml
mode: standalone
output_format: file
default_model: gemini-3.1-flash-image
timeout: 10m
default:
  base_url: "" # Empty falls back to OPENAI_BASE_URL
  api_key: "" # Empty falls back to OPENAI_API_KEY
models:
  gemini-3.1-flash-image:
    api: chat
    description: Image generation and editing
    tags: [text-rendering, multi-image]
```

Set `OPENAI_BASE_URL` and `OPENAI_API_KEY` in the environment that launches your agent client. You can put the real values in the config file instead, but restrict its permissions and keep it out of version control. See [images-mcp.example.yaml](images-mcp.example.yaml) for every option.

By default, images go to the system temp directory and come back as absolute paths. Set `output_dir` to save them somewhere else. Set `output_format` to `base64` to return image data without writing files, or to `both` to get both.

### 3. Connect your agent

Claude Desktop (`claude_desktop_config.json`), Cursor (`mcp.json`), and Claude Code (`.mcp.json` in your project) all use the JSON structure below. Merge the entry into your existing `mcpServers`:

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

Claude Code can also add it from the command line:

```sh
claude mcp add images-mcp -- images-mcp -config /absolute/path/images-mcp.yaml
```

For Codex, add this to `~/.codex/config.toml`:

```toml
[mcp_servers.images-mcp]
command = "images-mcp"
args = ["-config", "/absolute/path/images-mcp.yaml"]
```

Use the config file's real absolute path; `~` is not expanded. `command` should be an `images-mcp` the client can find on its PATH. If a GUI client can't find it, use the binary's absolute path instead. Leave your other MCP entries alone and restart the client after saving.

### 4. Check the connection

After restarting, you should see two tools: `generate_image` and `list_models`. Ask the agent to call `list_models` and check that the models and the default match your config. This only shows that the config loaded and the tools respond; it doesn't prove the relay can generate images or check S3 permissions.

From there, just ask: "Draw an icon for this project," or give the absolute path of a local image and say "Keep the subject and replace the background with mountains."

## MCP tools

### generate_image

Handles text-to-image and editing with multiple ordered images. The examples below use the configured default model.

Text to image:

```json
{"prompt":"Draw a simple sun icon for a weather app, with a transparent background"}
```

Multiple references. The first image is the one being edited; the rest serve as style or content references:

```json
{
  "prompt": "Keep the subject from the first image and use the palette and style of the second",
  "input_images": ["/absolute/path/subject.png", "/absolute/path/style.jpg"],
  "timeout": "15m"
}
```

| Parameter | Description |
| --- | --- |
| `prompt` | Required. What to generate or change. |
| `model` | Optional. A configured model ID; required if no default model is set. |
| `input_images` | Optional. Ordered reference images: absolute paths for local files, or HTTP(S) URLs. Up to 16 images, PNG / JPEG / WebP only. |
| `quality` | Optional. Quality level for Images models; allowed values come from the model config. |
| `size` | Optional. Preset or custom size for Images models, such as `1024x1024`. |
| `timeout` | Optional. Timeout for this request, such as `15m`. |

If `quality` or `size` is omitted, the model's configured default is used. When a model has `supports_custom_size` enabled, custom dimensions are rounded to the nearest multiple of 16, so `1920x1080` becomes `1920x1088`. After rounding, each side must be at most 3840px, the aspect ratio must fall between 1:3 and 3:1, and the total pixel count must be between 655,360 and 8,294,400. Malformed sizes, sizes outside those limits, and sizes the model doesn't allow are rejected with an error. An unsupported quality level falls back to the configured default. `api: chat` models such as Gemini ignore `quality` and `size`. Any of these adjustments is reported in the result's `notice` field.

With `file` or `both` output, standalone returns file paths and the remote server returns image URLs. When the output includes Base64, the result also carries native MCP image content. Failed generations are never retried automatically, and the model is never switched behind your back.

### list_models

Takes no arguments. Returns each configured model's ID, description, default flag, and editing support, plus optional `notes`, `tags`, quality levels and default quality, preset sizes and default size, and whether custom sizes are allowed. Agents use this to pick a model and size. The list comes straight from the config; images-mcp doesn't query the relay for available models.

## Remote deployment and Docker

On the remote host, use `mode: server` with the same relay and model settings, and set a service key in `MCP_SERVER_KEY` (separate from the relay key). `file` or `both` output needs an existing S3 bucket and credentials; MinIO, RustFS, R2, AWS, and other S3-compatible services work. Pure `base64` output doesn't need S3. See the `server` and `s3` sections of the config template for the fields.

Once the config file and environment variables are ready, start the container. Replace the example host path with your own; the config file must be readable by the container user (UID 10001).

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

In production, pin an image version or digest and put the service behind an HTTPS reverse proxy. Use `GET /healthz` for health checks.

The local agent's MCP entry keeps the same `command` / `args` shape; just replace `args` with:

```json
["-mode", "forward", "-forward-url", "https://images.example.com"]
```

Replace the example URL with your server's address, and set `IMAGES_MCP_FORWARD_KEY` in the client environment to the same service key the server uses. The local forward needs no relay key or S3 setup, and you still pass local image paths when editing. The server only speaks REST and can't be registered as an MCP server directly; agents always connect through the local forward over stdio.

## More docs

- [AGENTS.md](AGENTS.md): install, verification, and troubleshooting steps for agents
- [DESIGN.md](DESIGN.md): architecture
- [images-mcp.example.yaml](images-mcp.example.yaml): full config template

## Development checks

```sh
go test -race -cover ./...
go vet ./...
go build -o bin/images-mcp .
```

Tests run against local relay and S3 mocks and never call paid services.
