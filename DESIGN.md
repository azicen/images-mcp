# images-mcp design

## Purpose and compatibility

images-mcp is a stdio MCP server for image generation through OpenAI-compatible relays, such as NewAPI. OPENAI_BASE_URL and OPENAI_API_KEY provide defaults for all supported models, with family and request overrides. Clients that assume native Gemini endpoints, separate provider credentials, or a single image-generation endpoint (as in standard mcp-image setups) cannot express this relay contract reliably. This is a compatibility constraint, not a claim that every mcp-image version has identical behavior.

## Architecture and routing

The official github.com/modelcontextprotocol/go-sdk v1.8.0 registers generate_image and list_models with mcp.AddTool and runs with mcp.StdioTransport. Configuration, request construction, response extraction, and image storage remain small, independently testable components in package main.

* Route A: explicit `api` / `api_type` values `gemini` or `chat`, or model names beginning with gemini when unspecified (for example gemini-3.1-flash-image) use POST /v1/chat/completions with an OpenAI user message. Text-only requests use string content; editing requests use text and image_url content parts. Responses may contain Markdown images, data URIs, raw base64, or structured image_url parts.
* Route B: explicit `api` / `api_type` values `gpt`, `gpt-image`, or `images`, or model names beginning with gpt-image when unspecified (including gpt-image-2.5, gpt-image-2.5-flare, and gpt-image-2.5-sunburst) use POST /v1/images/generations for text-to-image and POST /v1/images/edits with multipart/form-data and an image file part when input_image is provided. Image responses accept data[].url and data[].b64_json.

API values are case-insensitive. Nonempty tool API arguments override the exact model configuration entry, and explicit selection overrides model prefixes. Empty strings mean unspecified. Invalid API values and aliases selecting different routes fail clearly; invalid model API configuration fails startup. Equivalent aliases may be supplied together.

* Unknown model families without explicit API selection fail before network I/O. No automatic retries: generation may incur charges. Requests propagate MCP cancellation and use a five-minute HTTP timeout.

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
| input_image | string | no | Local file path, HTTP(S) URL, image data URI, or raw base64 |
| size | string | no | Relay-supported image size; GPT Image routes only |
| quality | string | no | Relay-supported quality; GPT Image routes only |

One request returns every image supplied by the relay. Results contain JSON text and structured metadata: model, route, and images with absolute path, MIME type, and byte size. Failures are MCP tool errors. No API key or full upstream response is included in errors.

## Model discovery

At registration, the `generate_image` tool description includes the current default model (or explicitly indicates none), configured model IDs with their short descriptions sorted alphabetically, and guidance to call `list_models` for complete details and guidelines. Detailed notes are excluded from this summary. Ordering is deterministic for prompt caching. Unknown models that cannot be routed return an error listing configured IDs in alphabetical order; explicit API and prefix routing still allow unlisted models.

`list_models` has description: “List available image generation models, their API routes, capabilities, and guidelines.” Its input is an empty object with no required arguments. It returns JSON text containing a list sorted alphabetically by exact model ID, or `[]` when no models are configured. Each record contains:

| Field | Type | Meaning |
| --- | --- | --- |
| id | string | Exact configured model name |
| api | string | `chat` or `images`, derived from configured API aliases or model prefix |
| description | string | Short configured summary, empty if absent |
| notes | string | Detailed guidelines; omitted if empty |
| supports_edit | boolean | True for both supported routes |
| is_default | boolean | Whether the ID equals top-level default_model |

Discovery only lists exact `models` entries, never credentials or family/default objects. A default can refer to an unlisted model when its prefix or request API selection determines routing; it is shown in generation guidance but creates no synthetic discovery record.

## Parsing and storage

Markdown destinations, image data URIs, standalone HTTP(S) URLs, raw base64, and structured chat image parts feed the same loader. Downloads and local files are limited to 32 MiB; JSON responses to 64 MiB; at most 16 images per response. Base64 accepts padded/unpadded standard and URL-safe encodings. MIME type is detected from bytes, restricted to PNG/JPEG/GIF/WebP; misleading extensions or data URI labels do not control output types. Files use UTC timestamps and cryptographically random suffixes, exclusive creation, and owner-only permissions. Failed multi-image calls remove files already created by that call.

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

Configuration uses `default_model`, `default`, `families`, `models`, and `output_dir`. Top-level `default_model` is a model ID string, independent of the `default` credentials object. Missing or empty model arguments use this ID before routing, credential resolution, payload construction, and output metadata. If neither supplies an ID, generation fails before network I/O with guidance to supply `model` or configure `default_model`. Credential objects contain `base_url` and `api_key`. Model entries additionally accept `api` or `api_type`, for example `models: {"custom-model": {"api": "images"}}`. Model entries also accept `description` (short summary, roughly 60–80 characters) and optional `notes` (detailed, potentially multi-line guidelines, edge cases, and trade-offs). Neither field is supported on `default` or `families`. Configured custom names without a recognized prefix must specify `api` or `api_type`; unroutable entries fail startup so discovery can report a valid route for every record. The selected API determines both the request route and the credential family. YAML uses gopkg.in/yaml.v3 and JSON uses encoding/json; extensions select the parser. Files are limited to 1 MiB, YAML accepts a single document, and parse errors omit file contents to avoid exposing credentials. Unknown fields are ignored. Empty YAML and null configurations supply no overrides. Configuration and environment are captured at startup.

Output directory priority is file `output_dir`, then `IMAGE_OUTPUT_DIR`, then the OS temporary directory (`os.TempDir()`). Both settings default to `""`; absent or empty settings use `os.TempDir()`. The selected path is resolved with `filepath.Abs()`. Relative paths resolve from the server working directory, including paths supplied in a configuration file; the directory is made absolute at startup and created when saving images.

Stdout is reserved for MCP; startup errors go to stderr. SIGINT/SIGTERM cancel the server.

## Implementation and validation plan

1. Create module and pin SDK v1.8.0; inspect its actual tool and result APIs.
2. Implement configuration, deterministic routing/payloads, HTTP boundary, parsing/storage, and stdio entry point.
3. Add table-driven parser/router tests and httptest coverage for JSON/multipart payloads, downloads, validation, cancellation, and error handling; exercise MCP registration in memory.
4. Run go mod tidy, go test ./..., and go build -o bin/images-mcp .; also run race detection and vet.

## Scope and limitations

Model availability and optional size/quality values are relay-specific. The named models are routing examples, not a guarantee of provider availability. This implementation does not use native Gemini protocols, stream responses, or retry generation. Live relay testing requires credentials and remains separate from deterministic unit tests.
