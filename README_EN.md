# images-mcp

[English](README_EN.md) | [简体中文](README.md)

A single binary for image generation and editing through explicitly configured Chat and Images APIs, with shared image validation, timeouts and storage.

## Runtime modes

| Mode | Agent interface | Generation | Reference images |
| --- | --- | --- | --- |
| `standalone` (default) | Local stdio MCP | Local process calls relay | Local paths, HTTP(S) URLs, Data URIs |
| `server` | REST API | Server calls relay | JSON URLs/Data URIs or multipart files |
| `forward` | Local stdio MCP | Proxies to server | Reads local files and streams multipart transparently |

Both stdio modes (standalone and forward) expose `generate_image` and `list_models`; the server mode provides standard RESTful HTTP endpoints.

## Installation

Use a Go toolchain satisfying [go.mod](go.mod).

```sh
go install github.com/azicen/images-mcp@latest
# Alternatively, build from source:
go build -o bin/images-mcp .
```

Add the installation directory to the host client's PATH, or use a verified absolute executable path. `-config` requires an absolute path. Without it, configuration uses environment variables and defaults; no working-directory discovery occurs. `-h` exits successfully.

```sh
images-mcp -mode standalone -config /absolute/path/images-mcp.yaml
images-mcp -mode server -config /absolute/path/images-mcp.yaml -listen :8080
images-mcp -mode forward -forward-url https://images.example.com
```

Copy [images-mcp.example.yaml](images-mcp.example.yaml), then configure real relay credentials and supported model IDs. Every model must explicitly declare `api: chat` or `api: images`. Example model IDs do not establish relay availability. Never submit placeholder credentials. Prefer environment injection for secrets and restrict configuration permissions to `600`.

## Configuration

Runtime precedence: CLI > configuration file > environment > defaults.

| CLI | YAML | Environment | Default |
| --- | --- | --- | --- |
| `-mode` | `mode` | `IMAGES_MCP_MODE` | standalone |
| `-listen` | `server.listen` | `MCP_LISTEN` | :8080 |
| `-server-key` | `server.api_key` | `MCP_SERVER_KEY` / `SERVER_API_KEY` | empty |
| `-forward-url` | `forward.url` | `IMAGES_MCP_FORWARD_URL` | required in forward |
| `-forward-key` | `forward.api_key` | `IMAGES_MCP_FORWARD_KEY` / `FORWARD_KEY` / `MCP_FORWARD_KEY` | empty |
| `-forward-timeout` | `forward.timeout` | `IMAGES_MCP_FORWARD_TIMEOUT` | 15m |

Forward needs only the remote configuration. It does not initialize relay models, credentials, output directories or S3. Remote URLs support deployment path prefixes but reject user information, queries and fragments. Cross-origin redirects are blocked. Generation is never automatically retried, and caller cancellation reaches outgoing requests.

Generation settings:

- Resolve URL and key independently: request > model > family configuration > family environment > `default` > global environment. REST and forward reject request-level relay credentials and protocol overrides.
- Family environment: `GEMINI_BASE_URL` / `GEMINI_API_KEY`, `GPT_IMAGE_BASE_URL` / `GPT_IMAGE_API_KEY`. Global environment: `OPENAI_BASE_URL` / `OPENAI_API_KEY`.
- Timeout: request > model > `default.timeout` > top-level `timeout` > `IMAGE_TIMEOUT` > 10m. Accepts positive Go durations or integer seconds. Earlier host deadlines still apply.
- Global output format: top-level `output_format` > `default.output_format` > `IMAGE_OUTPUT_FORMAT` > `file`. Accepts case-insensitive `file`, `base64`, `both`; invalid settings prevent generation process startup.
- Standalone output directory: `output_dir` > `IMAGE_OUTPUT_DIR` > system temporary directory. File results contain absolute local paths.

YAML does not expand environment variables. Leave credential fields empty to use environment fallback and remove unused family placeholders that would shadow valid defaults. Successful `list_models` only verifies configuration/connectivity, not relay or S3 permissions.

## Agent clients

Local Codex configuration:

```toml
[mcp_servers.images-mcp]
command = "images-mcp"
args = ["-config", "/absolute/path/images-mcp.yaml"]
```

For remote generation, run forward on the Agent's machine:

```toml
[mcp_servers.images-mcp]
command = "images-mcp"
args = ["-mode", "forward", "-forward-url", "https://images.example.com"]
```

Supply `IMAGES_MCP_FORWARD_KEY` through the host environment. Claude Code, Cursor, Cline, Roo Code and Hermes use the equivalent command/args in their `mcpServers` configuration. Preserve existing entries and restart the client after changes. A REST URL is not a remote MCP endpoint.

Only run generation when authorized by the user:

```json
{"prompt":"A watercolor mountain landscape","timeout":"15m"}
```

```json
{"prompt":"Preserve the subject and change the background","input_images":["/absolute/path/main.png","/absolute/path/style.jpg"]}
```

The first reference is the primary image; later references provide style/context. Forward handles local paths automatically. Only Images models accept `size` and `quality`. Specify `model` when no default is configured. Do not ask the Agent to serialize large Base64 arguments.

## REST API

| Method/path | Result | Authentication |
| --- | --- | --- |
| `GET /healthz` | `{"status":"ok"}` | Public |
| `GET /v1/models` | Model record array | Required when a service key is configured |
| `POST /v1/generate` | `model` / `images` JSON | Required when a service key is configured |

Use `Authorization: Bearer <service-key>` or `X-API-Key`. Service and relay keys are separate. Use a strong service key and HTTPS reverse proxy for public deployments. Errors return `{"error":"message"}`: invalid parameters/MIME 400, unauthorized 401, excessive size 413, wrong method 405, relay/storage failures 500.

JSON fields: `prompt`, `model`, `size`, `quality`, `timeout`, `input_images`. Unknown fields are rejected. Plain JSON references must be HTTP(S) URLs or Data URIs; server-local paths are rejected.

```sh
curl --fail-with-body https://images.example.com/v1/generate \
  -H "Authorization: Bearer $IMAGES_MCP_FORWARD_KEY" \
  -H 'Content-Type: application/json' \
  --data '{"prompt":"A watercolor landscape"}'
```

Multipart accepts either a `request` JSON field or individual fields, never both. Individual `input_images` must contain a JSON array. Reference files using `part:<field-name>`; image order follows that array, regardless of upload order.

```sh
curl --fail-with-body https://images.example.com/v1/generate \
  -H "Authorization: Bearer $IMAGES_MCP_FORWARD_KEY" \
  -F 'request={"prompt":"Change the background","input_images":["part:file_0"]}' \
  -F 'file_0=@/absolute/path/main.png'
```

PNG/JPEG/WebP only; 32 MiB per image, 64 MiB combined, at most 16 images, 96 MiB request body. Multipart parameter fields total at most 1 MiB. Parsing is bounded and creates no temporary reference files. Duplicate fields, missing references and unreferenced files are rejected. References are used only for the current generation and are not separately uploaded to S3.

## S3 and outputs

Server `file` / `both` output requires a bucket and complete static S3 credentials before startup. Results never contain server-local paths. `images[].url` uses a public/CDN prefix or a presigned GET URL (default 24h, maximum 168h; temporary credentials may expire earlier). `both` also returns Base64. `base64` remains entirely in memory, initializes no S3 and writes no files. Forward neither downloads generated URLs nor saves files, and restores native MCP image content from Base64 results.

S3 environment fallback: `S3_BUCKET`, `S3_ENDPOINT` / `AWS_ENDPOINT_URL_S3`, `S3_REGION` / `AWS_REGION`, `AWS_ACCESS_KEY_ID`, `AWS_SECRET_ACCESS_KEY`, `AWS_SESSION_TOKEN`, `S3_USE_PATH_STYLE`, `S3_PUBLIC_URL_PREFIX`. File fields take precedence. Buckets must already exist and permit PutObject/GetObject/DeleteObject. Partial generation failures delete objects uploaded by that request and report cleanup errors. MinIO/RustFS/R2/AWS are supported; see the template for storage fields.

## Docker

The runtime image uses Debian Bookworm Slim and UID/GID `10001:10001`. Mount a verified absolute configuration path read-only, readable by that user. Never copy secrets into the image. Pin a release or digest in production.

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

Default CMD is `-mode server -listen :8080`. Custom arguments replace the entire CMD, so retain the mode/listen options. Recreate containers and reconnect clients after configuration changes. Container stdio uses `-i --no-healthcheck` without a TTY.

## Development

```sh
go test -race -cover ./...
go vet ./...
go build -o bin/images-mcp .
```

Tests use local relay/S3 mocks and cover all three modes, REST boundaries and the end-to-end MCP forward chain without billable requests. See [DESIGN.md](DESIGN.md) and [AGENTS.md](AGENTS.md).
