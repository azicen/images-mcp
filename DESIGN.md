# danqing design

## Purpose and compatibility

danqing is a stdio MCP server for image generation through a single OpenAI-compatible relay, such as NewAPI. It uses one OPENAI_BASE_URL and OPENAI_API_KEY for all supported models. Clients that assume native Gemini endpoints, separate provider credentials, or a single image-generation endpoint (as in standard mcp-image setups) cannot express this relay contract reliably. This is a compatibility constraint, not a claim that every mcp-image version has identical behavior.

## Architecture and routing

The official github.com/modelcontextprotocol/go-sdk v1.8.0 registers generate_image with mcp.AddTool and runs with mcp.StdioTransport. Configuration, request construction, response extraction, and image storage remain small, independently testable components in package main.

* Route A: model names beginning with gemini (for example gemini-3.1-flash-image) use POST /v1/chat/completions with an OpenAI user message. Text-only requests use string content; editing requests use text and image_url content parts. Responses may contain Markdown images, data URIs, raw base64, or structured image_url parts.
* Route B: model names beginning with gpt-image (including gpt-image-2.5, gpt-image-2.5-flare, and gpt-image-2.5-sunburst) use POST /v1/images/generations for text-to-image and POST /v1/images/edits with multipart/form-data and an image file part when input_image is provided. Image responses accept data[].url and data[].b64_json.
* Unknown model families fail before network I/O. No automatic retries: generation may incur charges. Requests propagate MCP cancellation and use a five-minute HTTP timeout.

The base URL may be an origin or include a /v1 suffix (including a relay path prefix); endpoint construction adds /v1 only when absent. Authorization is attached only to relay requests, never image downloads. Redirects must not forward relay credentials to another origin.

## generate_image schema

Object properties (additional properties disallowed by SDK inference):

| Property | Type | Required | Meaning |
| --- | --- | --- | --- |
| prompt | string | yes | Nonempty generation/edit instructions |
| model | string | yes | Gemini or GPT Image relay model name |
| input_image | string | no | Local file path, HTTP(S) URL, image data URI, or raw base64 |
| size | string | no | Relay-supported image size; GPT Image routes only |
| quality | string | no | Relay-supported quality; GPT Image routes only |

One request returns every image supplied by the relay. Results contain JSON text and structured metadata: model, route, and images with absolute path, MIME type, and byte size. Failures are MCP tool errors. No API key or full upstream response is included in errors.

## Parsing and storage

Markdown destinations, image data URIs, standalone HTTP(S) URLs, raw base64, and structured chat image parts feed the same loader. Downloads and local files are limited to 32 MiB; JSON responses to 64 MiB; at most 16 images per response. Base64 accepts padded/unpadded standard and URL-safe encodings. MIME type is detected from bytes, restricted to PNG/JPEG/GIF/WebP; misleading extensions or data URI labels do not control output types. Files use UTC timestamps and cryptographically random suffixes, exclusive creation, and owner-only permissions. Failed multi-image calls remove files already created by that call.

## Environment

* OPENAI_BASE_URL: required HTTP(S) relay origin or API prefix; query strings, fragments, and embedded credentials rejected.
* OPENAI_API_KEY: required relay bearer credential.
* IMAGE_OUTPUT_DIR: optional, defaults to ./images; resolved to an absolute directory and created as needed.

Stdout is reserved for MCP; startup errors go to stderr. SIGINT/SIGTERM cancel the server.

## Implementation and validation plan

1. Create module and pin SDK v1.8.0; inspect its actual tool and result APIs.
2. Implement configuration, deterministic routing/payloads, HTTP boundary, parsing/storage, and stdio entry point.
3. Add table-driven parser/router tests and httptest coverage for JSON/multipart payloads, downloads, validation, cancellation, and error handling; exercise MCP registration in memory.
4. Run go mod tidy, go test ./..., and go build -o bin/danqing .; also run race detection and vet.

## Scope and limitations

Model availability and optional size/quality values are relay-specific. The named models are routing examples, not a guarantee of provider availability. This implementation does not use native Gemini protocols, stream responses, or retry generation. Live relay testing requires credentials and remains separate from deterministic unit tests.
