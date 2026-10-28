# danqing

A Go MCP server for image generation and editing through one OpenAI-compatible relay. See [DESIGN.md](DESIGN.md) for routing, schema, and limits.

Build and test:

```sh
go mod tidy
go test -race -cover ./...
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

### Configuration files

Use `-config /absolute/path/to/danqing.yaml` (or `-c`) to select a file. Otherwise, the server checks its working directory for `danqing.yaml`, `danqing.yml`, then `danqing.json` and loads the first existing file. An invalid or unreadable selected file stops startup. Explicit paths must exist. Use `-h` for flag help.

Example `danqing.yaml`:

```yaml
default:
  base_url: https://relay.example/v1
  api_key: your-default-key
families:
  gemini:
    base_url: https://gemini-relay.example/v1
  gpt-image:
    api_key: your-gpt-key
models:
  custom-image-model:
    api: chat
    api_key: your-model-key
output_dir: /absolute/path/to/generated-images
```

Equivalent JSON is supported with a `.json` extension:

```json
{
  "default": {"base_url": "https://relay.example/v1", "api_key": "your-default-key"},
  "families": {"gemini": {"base_url": "https://gemini-relay.example/v1"}, "gpt-image": {"api_key": "your-gpt-key"}},
  "models": {"custom-image-model": {"api_type": "chat", "api_key": "your-model-key"}},
  "output_dir": "/absolute/path/to/generated-images"
}
```

MCP client configuration using a file:

```json
{
  "mcpServers": {
    "danqing": {
      "command": "/absolute/path/to/danqing/bin/danqing",
      "args": ["-config", "/absolute/path/to/danqing.yaml"],
      "env": {"OPENAI_API_KEY": "optional-fallback-key"}
    }
  }
}
```

The environment-only client example above also remains supported. Keep files containing API keys private.

### Credential resolution

Each field (`base_url` and `api_key`) resolves independently:

1. Per-request tool argument.
2. File `models` entry for the exact, case-sensitive model name.
3. File `families` entry (`gemini` or `gpt-image`).
4. Family environment (`GEMINI_BASE_URL` / `GEMINI_API_KEY` or `GPT_IMAGE_BASE_URL` / `GPT_IMAGE_API_KEY`).
5. File `default`.
6. Environment `OPENAI_BASE_URL` / `OPENAI_API_KEY`.

Family selection uses the explicit API type when supplied, otherwise a case-insensitive model prefix. Empty or whitespace-only credential fields fall through; partial overrides replace only the supplied field. Invalid nonempty winning values fail validation. Configuration is read at startup.

Output directory priority: file `output_dir`, then `IMAGE_OUTPUT_DIR`, then the OS temporary directory (`os.TempDir()`). Both output settings default to the empty string `""`; when neither supplies a nonempty value, `os.TempDir()` is used. The result is made absolute with `filepath.Abs()`. Relative paths resolve from the server's working directory, including file-supplied paths.

Call `generate_image` with:

```json
{"model":"gemini-3.1-flash-image","prompt":"A watercolor mountain landscape"}
```

For custom model names, set `api` or its alias `api_type` in the exact model configuration entry or tool arguments:

```json
{"model":"custom-image-model","api_type":"images","prompt":"A watercolor mountain landscape"}
```

API values are case-insensitive: `gemini` or `chat` selects `/chat/completions`; `gpt`, `gpt-image`, or `images` selects `/images/generations`, or `/images/edits` when `input_image` is provided. Tool API arguments override model API configuration; explicit API selection overrides model prefixes and selects the credential family (`gemini` or `gpt-image`). Without an explicit value, the existing `gemini` / `gpt-image` prefix detection applies. Empty strings mean unspecified. Invalid values or aliases selecting different routes return errors; invalid model API settings fail startup.

For GPT Image editing:

```json
{"model":"gpt-image-2.5","prompt":"Make the sky golden","input_image":"/absolute/path/to/input.png","size":"1024x1024"}
```

Gemini uses chat completions; GPT Image uses image generations or multipart image edits. `input_image` also accepts HTTP(S) URLs, image data URIs, and raw base64. Optional `size` and `quality` apply to GPT Image only and must be supported by your relay. Successful results contain absolute image paths, detected MIME types, byte counts, model, and route in both JSON text and structured MCP content. Errors are returned as MCP tool errors. Stdout carries only MCP traffic.

Requests are not retried automatically. Model availability depends on your relay. Tests use local HTTP fixtures and do not require credentials or make paid generation calls.

SDK reference: [official Go SDK v1.8.0](https://github.com/modelcontextprotocol/go-sdk/tree/v1.8.0).
