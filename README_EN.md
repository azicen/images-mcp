[English](README_EN.md) | [简体中文](README.md)

# images-mcp

Generate and edit images through OpenAI-compatible relays, with local file or in-memory Base64 output. One Go binary connects to MCP clients over stdio or HTTP, with S3 URLs for HTTP file output.

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

## HTTP transport and S3 output

The default transport remains stdio. To enable the official Go SDK Streamable HTTP server, add this to your existing relay/model configuration:

```yaml
server:
  transport: http
  listen: "127.0.0.1:8080"
  api_key: "" # inject a real service key with MCP_SERVER_KEY
  enable_sse: false
s3:
  endpoint: https://storage.example
  bucket: generated-images
  region: us-east-1
  access_key_id: "" # AWS_ACCESS_KEY_ID
  secret_access_key: "" # AWS_SECRET_ACCESS_KEY
  use_path_style: true
  key_prefix: images
  public_url_prefix: ""
  presign_ttl: 24h
```

```sh
images-mcp -config /absolute/path/to/images-mcp.yaml -http 127.0.0.1:8080
```

`-http` overrides transport and listen address; `-server-key` overrides the service access key. Otherwise configuration fields take precedence over `MCP_TRANSPORT`, `MCP_LISTEN`, and `MCP_SERVER_KEY` / `SERVER_API_KEY`. Prefer environment variables or private configuration files over putting secrets in command arguments.

Connect to `http://127.0.0.1:8080/mcp`, or `/sse` when `enable_sse` is true. Send `Authorization: Bearer <service-key>` on every request, including SSE session POSTs. `X-API-Key` and `?api_key=` are also supported, in that priority order after Bearer. Missing/incorrect tokens return JSON 401. An empty service key disables authentication. Configure a strong key and HTTPS at your reverse proxy for public deployments; prefer headers since query tokens may appear in logs. `/healthz` is unauthenticated and returns `{"status":"ok"}`. Request bodies are limited to 96 MiB.

HTTP `file` / `both` output requires an S3 bucket and complete static credentials. Invalid or missing storage configuration fails before listening. Create the bucket first and allow PutObject, GetObject, and DeleteObject. MinIO, RustFS, R2, and AWS S3 are supported: specify an endpoint for custom services, omit it for AWS, and generally use region `auto` for R2. Environment fallbacks: `S3_ENDPOINT` / `AWS_ENDPOINT_URL_S3`, `S3_BUCKET`, `S3_REGION` / `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `S3_USE_PATH_STYLE`, and `S3_PUBLIC_URL_PREFIX`. An explicit `use_path_style: false` overrides the environment.

Objects use `{key_prefix}/YYYY/MM/DD/{random_hex}{ext}` and the detected image Content-Type. Results contain `images[].url` and omit `path`. A configured public URL prefix is joined with the object key; otherwise the server creates a presigned GET URL, valid for 24h by default (`presign_ttl`, positive Go duration up to 168h). Temporary credentials can expire earlier. Public/CDN access must already be configured; the server does not change bucket policies. `both` also returns Base64 and native MCP image content. Failed multi-image calls attempt to delete objects uploaded by that call and report cleanup failures. Configure bucket lifecycle policies for successful objects.

HTTP `base64` output needs no S3 and writes nothing to disk. Stdio retains local absolute paths. Restart the server and client connections after configuration changes. `list_models` does not validate relay or S3 credentials; only authorized generation tasks perform real uploads. See the [complete configuration template](images-mcp.example.yaml) for all fields.

## Docker deployment

The image `ghcr.io/azicen/images-mcp` uses Debian Bookworm Slim (`debian:bookworm-slim`) for runtime and supports `linux/amd64` and `linux/arm64`. After a release is published, pull `latest`; pin a published version or digest for production:

```sh
docker pull ghcr.io/azicen/images-mcp:latest
```

Prepare `/etc/images-mcp/config.yaml` on the host. Use relay-supported model IDs with explicit `api: chat` or `api: images`. For environment-based relay credentials, remove placeholder credentials from `default`, `families`, and individual models so they cannot override the environment. Set `output_format: file`. Configuration is excluded from the image; ensure UID/GID `10001:10001` can read the mounted file (for example, owner 10001 with mode 600).

Securely populate the following real environment variables in your shell first. `-e NAME` inherits values without putting secrets in command text:

```sh
docker run -d --name images-mcp --restart unless-stopped \
  -p 127.0.0.1:8080:8080 \
  --mount type=bind,src=/etc/images-mcp/config.yaml,dst=/etc/images-mcp/config.yaml,readonly \
  -e OPENAI_BASE_URL -e OPENAI_API_KEY -e MCP_SERVER_KEY \
  -e S3_BUCKET -e S3_ENDPOINT -e S3_REGION -e S3_USE_PATH_STYLE \
  -e AWS_ACCESS_KEY_ID -e AWS_SECRET_ACCESS_KEY -e AWS_SESSION_TOKEN \
  ghcr.io/azicen/images-mcp:latest \
  -config /etc/images-mcp/config.yaml -http :8080
curl --fail http://127.0.0.1:8080/healthz
```

HTTP `file` / `both` requires an existing S3 bucket and complete credentials. Set an endpoint for custom storage; R2 generally uses region `auto`. Set a separate strong `MCP_SERVER_KEY`. Connect clients to `/mcp` with `Authorization: Bearer <service-key>` and use an HTTPS reverse proxy for public access. Docker administrators can inspect container environments, so restrict host access. File configuration takes precedence over environment fallbacks; see the HTTP/S3 section above.

The default CMD is `-http :8080`. Arguments after the image replace the entire CMD: when passing a configuration path, also pass `-http :8080` or enable HTTP in that configuration. Changing the port requires updating the port mapping and `--health-cmd`. `/healthz` checks the local server only, not relay or storage permissions. Recreate the container and reconnect clients after configuration changes.

With `output_format: base64`, S3 is unnecessary. For stdio, use `docker run --rm -i --no-healthcheck` without `-t`, mount the same configuration, and pass only `-config /etc/images-mcp/config.yaml` after the image; set `server.transport: stdio` in the file. Local file output also needs a writable output-directory mount accessible to UID 10001. Disable or replace the default health check for stdio or HTTP ports other than 8080.

Build locally with `docker build -t images-mcp:local .`. GitHub Actions tests and builds main pushes and PRs without publishing. A `v*` tag publishes version, minor, major, and `latest` tags. Manual runs default to no push; selecting `push` publishes the selected branch tag. Manual runs on version tags still follow the tag publishing condition. Prerelease SemVer tags follow metadata-action rules; all `v*` tags still update `latest`.


## Upload local reference images over HTTP

Remote servers cannot access the agent's local filesystem. In stdio mode, pass absolute local paths directly to `generate_image.input_images`. In HTTP mode, call the `upload_image` MCP tool for instructions, then run curl on the machine containing the image:

```sh
curl --fail-with-body -X POST "<server_url>/upload" \
  -H "Authorization: Bearer <server_api_key>" \
  -F "file=@<local_path>"

curl --fail-with-body -X POST "<server_url>/upload" \
  -H "X-API-Key: <server_api_key>" \
  -H "Content-Type: image/png" --data-binary "@<local_path>"
```

```json
{"url":"https://s3.example.com/images/abc.png","mime_type":"image/png","bytes":1048576}
```

Replace placeholders with actual values; omit authentication headers only when no server key is configured. `/upload` accepts one PNG/JPEG/WebP image up to 32 MiB, using multipart field `file` or `image`, or a binary body. It shares MCP authentication (Bearer, X-API-Key, or api_key query parameter; prefer headers). S3 is required even when generation uses base64 output; configuring S3 enables uploads without changing that output format. No S3 returns 400, invalid/empty uploads return 400, oversized uploads 413, non-POST methods 405, and storage failures 500. Uploads create S3 objects; do not retry automatically.

`upload_image` is automatically registered and exposed only in HTTP mode; local stdio mode exposes only `generate_image` and `list_models`.

`upload_image` returns text instructions only and does not upload the file itself. HTTP local-path errors point back to this tool and `/upload` so the agent can recover by uploading and using the returned URL. Never substitute a server-side path for a client file.
