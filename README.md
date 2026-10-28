# danqing

A Go MCP server for image generation and editing through one OpenAI-compatible relay. See [DESIGN.md](DESIGN.md) for routing, schema, and limits.

Build and test:

```sh
go mod tidy
go test ./...
go build -o bin/danqing .
```

Configure your MCP client (use absolute paths):

```json
{
  "mcpServers": {
    "danqing": {
      "command": "/absolute/path/to/danqing/bin/danqing",
      "env": {
        "OPENAI_BASE_URL": "https://relay.example/v1",
        "OPENAI_API_KEY": "your-default-key",
        "GEMINI_BASE_URL": "https://gemini-relay.example/v1",
        "GEMINI_API_KEY": "optional-gemini-key",
        "GPT_IMAGE_BASE_URL": "https://gpt-relay.example/v1",
        "GPT_IMAGE_API_KEY": "optional-gpt-key",
        "IMAGE_OUTPUT_DIR": "/absolute/path/to/generated-images"
      }
    }
  }
}
```

### Credential Resolution Hierarchy
Each field (`base_url` and `api_key`) resolves independently:
1. **Per-request parameters**: `base_url` or `api_key` passed in the `generate_image` tool call.
2. **Model family environment**:
   - Gemini models (`gemini*`): `GEMINI_BASE_URL`, `GEMINI_API_KEY`.
   - GPT Image models (`gpt-image*`): `GPT_IMAGE_BASE_URL`, `GPT_IMAGE_API_KEY`.
3. **Global default**: `OPENAI_BASE_URL`, `OPENAI_API_KEY`.

`IMAGE_OUTPUT_DIR` defaults to `./images` relative to the server's working directory.

Call `generate_image` with:

```json
{"model":"gemini-3.1-flash-image","prompt":"A watercolor mountain landscape"}
```

For GPT Image editing:

```json
{"model":"gpt-image-2.5","prompt":"Make the sky golden","input_image":"/absolute/path/to/input.png","size":"1024x1024"}
```

Gemini uses chat completions; GPT Image uses image generations or multipart image edits. `input_image` also accepts HTTP(S) URLs, image data URIs, and raw base64. Optional `size` and `quality` apply to GPT Image only and must be supported by your relay. Successful results contain absolute image paths, detected MIME types, byte counts, model, and route in both JSON text and structured MCP content. Errors are returned as MCP tool errors. Stdout carries only MCP traffic.

Requests are not retried automatically. Model availability depends on your relay. Tests use local HTTP fixtures and do not require credentials or make paid generation calls.

SDK reference: [official Go SDK v1.8.0](https://github.com/modelcontextprotocol/go-sdk/tree/v1.8.0).
