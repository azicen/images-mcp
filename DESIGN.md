# images-mcp design

## Purpose and compatibility

images-mcp is a stdio and HTTP MCP server for image generation through OpenAI-compatible relays, such as NewAPI. OPENAI_BASE_URL and OPENAI_API_KEY provide defaults for all supported models, with family and request overrides. Clients that assume native Gemini endpoints, separate provider credentials, or a single image-generation endpoint (as in standard mcp-image setups) cannot express this relay contract reliably. This is a compatibility constraint, not a claim that every mcp-image version has identical behavior.

## Architecture and routing

The official github.com/modelcontextprotocol/go-sdk v1.8.0 registers generate_image and list_models with mcp.AddTool and runs with mcp.StdioTransport or the SDK HTTP handlers. Configuration, request construction, response extraction, and image storage remain small, independently testable components in package main.

* Route A: explicit `api` / `api_type` values `gemini` or `chat`, or model names beginning with gemini when unspecified (for example gemini-3.1-flash-image) use POST /v1/chat/completions with an OpenAI user message. Text-only requests use string content; editing requests use text and image_url content parts. Responses may contain Markdown images, data URIs, raw base64, or structured image_url parts.
* Route B: explicit `api` / `api_type` values `gpt`, `gpt-image`, or `images`, or model names beginning with gpt-image when unspecified (including gpt-image-2.5, gpt-image-2.5-flare, and gpt-image-2.5-sunburst) use POST /v1/images/generations for text-to-image and POST /v1/images/edits with multipart/form-data and one or more image file parts when normalized inputs are nonempty. Image responses accept data[].url and data[].b64_json.

API values are case-insensitive. Nonempty tool API arguments override the exact model configuration entry, and explicit selection overrides model prefixes. Empty strings mean unspecified. Invalid API values and aliases selecting different routes fail clearly; invalid model API configuration fails startup. Equivalent aliases may be supplied together.

* Unknown model families without explicit API selection fail before network I/O. No automatic retries: generation may incur charges. Requests propagate MCP cancellation and use a configurable shared context deadline. The HTTP client has Timeout: 0, with no fixed client timeout.

The base URL may be an origin or include a /v1 suffix (including a relay path prefix); endpoint construction adds /v1 only when absent. Authorization is attached only to relay requests, never image downloads. Redirects must not forward relay credentials to another origin.

## generate_image schema

Object properties (additional properties disallowed by SDK inference):

| Property | Type | Required | Meaning |
| --- | --- | --- | --- |
| prompt | string | yes | Nonempty generation/edit instructions |
| model | string | no | Relay model name; omitted or empty uses top-level default_model; custom names require explicit API selection |
| api | string | no | API route override: gemini/chat or gpt/gpt-image/images |
| api_type | string | no | Alias for api |
| base_url | string | no | Per-request relay base URL override |
| api_key | string | no | Per-request bearer credential override |
| input_images | array of strings | no | Ordered paths, HTTP(S) URLs, Data URIs, or raw base64; first image is primary, others are style/context references |
| size | string | no | Relay-supported image size; GPT Image routes only |
| quality | string | no | Relay-supported quality; GPT Image routes only |

One request returns every image supplied by the relay. Results contain JSON text and structured metadata: model, route, and images with MIME type and byte size, plus an absolute path and/or Base64 according to the global output format. Failures are MCP tool errors. No API key or full upstream response is included in errors.

## Model discovery

At registration, the `generate_image` tool description includes the current default model (or explicitly indicates none), configured model IDs with their short descriptions sorted alphabetically, and guidance to call `list_models` for complete details and guidelines. Detailed notes are excluded from this summary. Ordering is deterministic for prompt caching. Unknown models that cannot be routed return an error listing configured IDs in alphabetical order; explicit API and prefix routing still allow unlisted models.

`list_models` has description: “List available image generation models, their capabilities, and guidelines.” Its input is an empty object with no required arguments. It returns JSON text containing a list sorted alphabetically by exact model ID, or `[]` when no models are configured. Each record contains:

| Field | Type | Meaning |
| --- | --- | --- |
| id | string | Exact configured model name |
| description | string | Short configured summary, empty if absent |
| notes | string | Detailed guidelines; omitted if empty |
| supports_edit | boolean | True for both supported routes |
| is_default | boolean | Whether the ID equals top-level default_model |

Discovery only lists exact `models` entries, never credentials or family/default objects. A default can refer to an unlisted model when its prefix or request API selection determines routing; it is shown in generation guidance but creates no synthetic discovery record.

## Parsing and storage

Markdown destinations, image data URIs, standalone HTTP(S) URLs, raw base64, and structured chat image parts feed the same loader. Downloads and local files are limited to 32 MiB; JSON responses to 64 MiB; at most 16 images per response. Base64 accepts padded/unpadded standard and URL-safe encodings. MIME type is detected from bytes: input accepts only PNG/JPEG/WebP; saved output accepts PNG/JPEG/GIF/WebP; misleading extensions or data URI labels do not control output types. Files use UTC timestamps and cryptographically random suffixes, exclusive creation, and owner-only permissions. Failed multi-image calls remove files already created by that call.

## Configuration

Each field resolves independently, from highest to lowest priority:

1. Nonempty generate_image arguments `base_url` and `api_key`.
2. File `models` entry matching the exact, case-sensitive model name.
3. File `families` entry (`gemini` or `gpt-image`, selected by explicit API type or case-insensitive model prefix).
4. Family environment variables: `GEMINI_BASE_URL` / `GEMINI_API_KEY` or `GPT_IMAGE_BASE_URL` / `GPT_IMAGE_API_KEY`.
5. File `default` credentials.
6. Environment `OPENAI_BASE_URL` / `OPENAI_API_KEY`.

Missing, empty, or whitespace-only values fall through to the next level. Partial overrides replace only the supplied field: a family base URL alone uses the default API key, and a request API key alone uses the family or default base URL. Environment values are captured at startup; credentials are resolved and validated before each request, so defaults need not be configured when overrides supply both fields. Invalid nonempty resolved values fail rather than silently falling back.

The resolved base URL must be a nonempty HTTP(S) URL without embedded credentials, query, or fragment; trailing slashes are removed and `/v1` is appended only when absent. The resolved API key must be a nonempty printable ASCII token without whitespace. Surrounding whitespace is trimmed. Validation errors never include credential values. Credentials are attached only to the generation/edit request header, never the payload, image downloads, saved metadata, or filenames.

CLI flags `-config PATH` and `-c PATH` select an explicit file; the last occurrence wins. Without either flag, startup searches the working directory for `images-mcp.yaml`, `images-mcp.yml`, then `images-mcp.json`, loading only the first existing file. Missing discovery candidates are skipped; an explicit missing file, unreadable file, invalid file, or unsupported extension fails startup. Help exits successfully; unknown flags, empty explicit paths, and positional arguments fail.

Configuration uses `default_model`, `default`, `families`, `models`, `output_dir`, and `output_format`. Top-level `default_model` is a model ID string, independent of the `default` credentials object. Missing or empty model arguments use this ID before routing, credential resolution, payload construction, and output metadata. If neither supplies an ID, generation fails before network I/O with guidance to supply `model` or configure `default_model`. Credential objects contain `base_url` and `api_key`. Model entries additionally accept `api` or `api_type`, for example `models: {"custom-model": {"api": "images"}}`. Model entries also accept `description` (short summary, roughly 60–80 characters) and optional `notes` (detailed, potentially multi-line guidelines, edge cases, and trade-offs). Neither field is supported on `default` or `families`. Configured custom names without a recognized prefix must specify `api` or `api_type`; unroutable entries fail startup so discovery can report a valid route for every record. The selected API determines both the request route and the credential family. YAML uses gopkg.in/yaml.v3 and JSON uses encoding/json; extensions select the parser. Files are limited to 1 MiB, YAML accepts a single document, and parse errors omit file contents to avoid exposing credentials. Unknown fields are ignored. Empty YAML and null configurations supply no overrides. Configuration and environment are captured at startup.

Output format is global only: top-level `output_format` > `default.output_format` > `IMAGE_OUTPUT_FORMAT` > `file`. Startup validates nonempty settings against `file`, `base64`, and `both`, case insensitively, and captures the normalized lowercase value. No request/model setting or alias is provided. Family credential entries do not participate in format resolution.

Images are downloaded or decoded once in memory. In `file` mode they are saved locally and only `path` is returned, saving context tokens. In `base64` mode bytes are encoded with standard Base64 and returned as `base64`; the save path returns before directory creation or any file writes, including temporary files. In `both` mode the result contains both fields. Empty `path` and `base64` fields are omitted. Base64 modes also attach native MCP ImageContent using the original bytes and MIME type, alongside JSON text and structured output. Failed generations remove only files already created by the request.

Output directory priority is file `output_dir`, then `IMAGE_OUTPUT_DIR`, then the OS temporary directory (`os.TempDir()`). Both settings default to `""`; absent or empty settings use `os.TempDir()`. The selected path is resolved with `filepath.Abs()`. Relative paths resolve from the server working directory, including paths supplied in a configuration file; the directory is made absolute at startup and created when saving images.

Stdout is reserved for MCP; startup errors go to stderr. SIGINT/SIGTERM cancel the server.

## Implementation and validation plan

1. Create module and pin SDK v1.8.0; inspect its actual tool and result APIs.
2. Implement configuration, deterministic routing/payloads, HTTP boundary, parsing/storage, and stdio entry point.
3. Add table-driven parser/router tests and httptest coverage for JSON/multipart payloads, downloads, validation, cancellation, and error handling; exercise MCP registration in memory.
4. Run go mod tidy, go test ./..., and go build -o bin/images-mcp .; also run race detection and vet.

## Scope and limitations

Model availability and optional size/quality values are relay-specific. The named models are routing examples, not a guarantee of provider availability. This implementation does not use native Gemini protocols, stream responses, or retry generation. Live relay testing requires credentials and remains separate from deterministic unit tests.

## Multi-image input normalization and assembly

`generateInput` lives in main.go. `normalizedInputImages` validates nonblank `input_images` entries without deduplication, preserving order and original array indices for errors. More than 16 images fail before network I/O. No inputs selects text-to-image; any inputs select editing. The first is the primary image to edit; subsequent images are style/context references, with model behavior guided by the prompt.

The shared input loader in images.go enforces 32 MiB per image and `maxTotalInputBytes = 64 << 20` decoded bytes per request. Loading stops at the first error or cumulative overflow, before loading subsequent references or sending a generation request. Error context identifies the original zero-based `input_images[i]`; base64 contents are not echoed. Input GIF/SVG are rejected regardless of extensions or Data URI labels; output GIF remains supported.

In relay.go, GPT Image sends multipart POST /v1/images/edits: one input uses `name="image"`; multiple inputs all use `name="image[]"`. Each file part has a generated filename with detected extension and its own Content-Type. Gemini sends POST /v1/chat/completions: user content starts with the prompt text part, followed by ordered image_url parts using detected MIME and standard base64 Data URIs.

Tests cover single-image arrays, repeated multi-image fields and bytes, ordered Gemini parts, count limits, rejected MIME, indexed errors, exact aggregate budget and overflow, and output GIF preservation. Run `go test -race -cover ./...`, `go vet ./...`, and `go build -o bin/images-mcp .`.


## Request timeout

`generateInput.Timeout` is exposed as the optional `timeout` string argument.
`fileConfig.Timeout`, `fileCredentials.Timeout` (under `default`), and
`fileModel.Timeout` accept the same string in YAML and JSON.

Resolution: request `timeout` > exact `models.<id>.timeout` >
`default.timeout` > top-level `timeout` > `IMAGE_TIMEOUT` > `10m`.
The default model is selected before resolving its timeout. Family timeout
settings do not participate. Empty strings fall through; an invalid selected
value fails without fallback. `parseTimeout` accepts positive Go durations
(`10m`, `15m`, `600s`, `1h`) and integer seconds strings (`"600"`),
rejecting zero, negative values, malformed values, and duration overflow.
Startup validates all nonempty top-level, default, and model timeout settings.
The environment value is validated when selected for a request.

`service.generate` creates one `context.WithTimeout` and defers cancellation
before preparing the request. This shared budget covers input downloads,
relay POST and response reading, and output downloads and saving, rather than
resetting for each stage. Existing context checks guard local saves; local
filesystem system calls are synchronous and cannot be interrupted mid-call.
An earlier parent deadline or MCP cancellation remains effective.

## HTTP transport, authentication, and object storage

Transport defaults to `stdio`. `server.transport: http` or `-http <address>` selects an `http.Server` using the official SDK's `NewStreamableHTTPHandler` on `/mcp`. Optional `NewSSEHandler` serves `/sse` and its session POST requests when `server.enable_sse` is true. Both handlers share the existing MCP server/tool definitions and explicitly allow request bodies up to 96 MiB. `/healthz` returns JSON `{"status":"ok"}` without authentication. HTTP header reads have a 10s deadline; no write timeout truncates long image-generation or SSE responses. Process cancellation gracefully shuts down HTTP, with forced connection closure after a 5s shutdown budget, and joins the shutdown watcher even on listen failure.

CLI `-http` overrides transport/listen and `-server-key` overrides the HTTP service key. Otherwise file settings precede `MCP_TRANSPORT`, `MCP_LISTEN`, and `MCP_SERVER_KEY` / `SERVER_API_KEY`; default listen is `:8080`. An explicit empty CLI key disables authentication. Service authentication is separate from relay credentials. Middleware protects both MCP handlers, including SSE session POSTs, accepting Bearer (case-insensitive scheme), X-API-Key, or api_key query, in that precedence order. Tokens are hashed to fixed-size SHA-256 values before `subtle.ConstantTimeCompare`. Missing/wrong tokens return JSON 401; an empty configured key disables authentication. Public deployments should use a strong key and HTTPS termination. Query credentials should be avoided in logs.

Before starting any listener, HTTP `file` / `both` output requires a nonempty bucket, complete static credentials, valid optional HTTP(S) endpoint/public-prefix URLs, valid path-style environment boolean, and a positive presign duration no greater than 7 days. Credentials are not fetched through implicit AWS instance-role chains. Defaults: region `us-east-1`, key prefix `images`, TTL `24h`. File values override S3 environment fallbacks; explicit path-style false is preserved. Environment variables: S3_ENDPOINT / AWS_ENDPOINT_URL_S3, S3_BUCKET, S3_REGION / AWS_REGION, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY, AWS_SESSION_TOKEN, S3_USE_PATH_STYLE, S3_PUBLIC_URL_PREFIX. Startup performs structural validation only, without creating buckets or making credential-validation requests.

`storage.go` uses AWS SDK v2 S3 PutObject, DeleteObject, and PresignGetObject with static credentials, custom endpoint and path-style options. Objects are named `{key_prefix}/YYYY/MM/DD/{random_hex}{ext}`, using UTC dates, 128 random bits, and the MIME-derived extension. Content-Type comes from actual image bytes. S3 operations use request cancellation/deadlines and do not automatically retry. An explicit public URL prefix produces a URL with escaped object path; otherwise a signed GET URL uses presign_ttl. Bucket/CDN policy setup and lifecycle retention are the operator's responsibility; temporary credentials may shorten URL lifetime.

In HTTP mode, `file` returns URL/MIME/bytes, `both` adds Base64 and native MCP image content, and neither returns local paths nor writes disk files. `base64` bypasses all S3 initialization and persists nothing. Stdio bypasses S3 completely and keeps existing local file behavior. Internal object keys are never serialized in tool results. Upload success followed by URL-signing failure deletes that object; any subsequent multi-image failure deletes all objects already uploaded by the call. Cleanup uses cancellation-independent contexts bounded to 30s per object and joins cleanup errors into the tool error. Successful objects are not deleted automatically.

## Container build and release pipeline

The Dockerfile separates compilation from runtime. The builder uses `--platform=$BUILDPLATFORM golang:alpine` with git, CA certificates, and timezone data. Copying go.mod/go.sum before source preserves the dependency-download layer. BuildKit supplies TARGETOS/TARGETARCH; CGO is disabled and `-trimpath -ldflags="-s -w"` produces `/out/images-mcp` without a C runtime dependency. Only that binary crosses into Alpine 3.21, which installs runtime CA certificates and timezone data. UID/GID 10001 runs the process without root privileges. The requested floating builder tag tracks Go releases; go.mod remains the minimum toolchain contract.

`.dockerignore` excludes Git/CI metadata, Markdown, YAML configuration, environment files, keys, binaries, logs, caches, and generated images. Runtime configuration is a read-only bind mount with an absolute container path; relay, service, and S3 credentials are injected at deployment, never embedded in layers. The mounted file must be readable by UID 10001. Configuration fields retain precedence over environment fallbacks, so template credential placeholders must be removed before relying on environment injection.

Exec-form ENTRYPOINT runs images-mcp directly for signal delivery. CMD selects HTTP on :8080; overriding CMD requires explicitly retaining HTTP flags or enabling HTTP in configuration. Port 8080 is exposed, and BusyBox wget probes the unauthenticated /healthz endpoint every 30 seconds. This indicates local HTTP availability only. Stdio deployments disable the health check and use stdin without a TTY; custom HTTP ports require a matching probe. HTTP file/both retains mandatory S3 validation, while base64 needs no storage. Public access uses a separate service key and HTTPS termination.

`.github/workflows/docker.yml` first runs `go test -race -cover ./...` with the Go version from go.mod. Only a successful test job enables the QEMU/Buildx build for linux/amd64 and linux/arm64. Main pushes and pull requests build without registry login or publication. Version tags (`v*`) publish to GHCR, and workflow_dispatch offers a boolean push input defaulting to false. The shared login/push condition also treats manual runs on v-prefixed tags as publication. The workflow grants read-only contents access globally and packages:write only to the build job; GHCR uses the ephemeral GITHUB_TOKEN.

Metadata-action derives SemVer version/minor/major tags and branch tags; v-prefixed tags additionally enable latest. Prerelease SemVer expansion follows metadata-action rules, but the explicit latest rule still applies to every v-prefixed tag. Build-push-action uses GitHub Actions cache import/export (mode=max). No credentials or deployment configuration enter the build context. Publishing the image does not deploy a service or validate paid image generation or S3 permissions.
