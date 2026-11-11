# REST image responses / REST 图片响应

`GET /healthz` is public. `GET /v1/models` and `POST /v1/generate` accept the configured service key via `Authorization: Bearer ...` or `X-API-Key`. Generation errors return `application/json` with `{"error":"..."}` and a non-2xx status. No automatic retries.

探活无需认证；模型与生成端点使用服务密钥。请求 JSON 字段为 `prompt`、`model`、`size`、`quality`、`timeout`、`input_images`。参考图支持 URL、Data URI 或 multipart/form-data 中的 `part:file_N` 引用；普通本地路径由 forward 读取并发送，REST 不读取服务器路径。multipart 参数使用单个 `request` JSON 字段或独立字段，不得混用。最多 16 张 PNG/JPEG/WebP，单图 32 MiB、合计 64 MiB，请求体 96 MiB，参数合计 1 MiB。

`output_format` selects local Agent output for standalone and forward only. Server image responses always use raw binary `multipart/mixed`, even when its configuration says `base64` or `both`. Forward rejects JSON success responses. Non-2xx errors retain the JSON error envelope.

`output_format` 只选择 standalone 与 forward 的本机 Agent 输出；server 成功图片响应恒定为 multipart/mixed，不编码或传输 Base64。

## multipart/mixed

Successful responses have `Content-Type: multipart/mixed; boundary=<random-boundary>`. The first part is JSON with `Content-Type: application/json` and `Content-ID: <manifest>`. Its schema is:

```json
{"model":"configured-model-id","notice":"optional notice","images":[{"content_id":"image-0","mime_type":"image/png","bytes":1024}]}
```

Subsequent parts are raw image bytes in manifest order, with `Content-Type` matching `mime_type` and `Content-ID: <image-0>`, `<image-1>`, etc. IDs are zero-based and unique, with exactly one part per manifest entry. There are no filenames, remote paths, URLs, or transfer encodings. The manifest contains only model, optional notice, and image content_id/mime_type/bytes metadata. It never contains output_format, Base64, URL, or path.

`notice` is omitted if empty. 成功响应首段为 manifest，其后每张图片恰好一个原始二进制 part，顺序与 manifest 一致。直接 REST 客户端须使用 MIME multipart 解析器读取响应。

For example (CRLF line endings; bytes shown symbolically):

```text
--boundary
Content-Type: application/json
Content-ID: <manifest>

{"model":"configured-model-id","images":[{"content_id":"image-0","mime_type":"image/png","bytes":1024}]}
--boundary
Content-Type: image/png
Content-ID: <image-0>

[1024 raw bytes]
--boundary--
```

Limits: 16 images, 32 MiB per image, 64 MiB total image bytes, 1 MiB manifest, 160 MiB response body including MIME framing. MIME headers also use Go's bounded multipart parser. Forward requires matching declared and actual byte lengths, matching detected MIME (PNG/JPEG/GIF/WebP), unique ordered IDs, no missing/duplicate/unreferenced parts. Unknown manifest fields are rejected. A truncated transfer is a failure, even if some images arrived.

## Local forward output / 本机输出

After validating the complete multipart response, forward applies its own output_format: `file` saves local files and returns paths without Base64 or MCP ImageContent; `base64` encodes the received bytes locally and returns Base64 plus MCP ImageContent without creating files or directories; `both` saves files and also returns Base64 and MCP ImageContent. Standalone keeps the same local output behavior.

forward 的本机 output_format 独立决定 MCP 结果形式，服务端不保存生成文件。`file` / `both` 在完整校验响应后才写文件，目录为顶层 `output_dir`，回退 `IMAGE_OUTPUT_DIR` 和系统临时目录，启动时解析为绝对路径；新目录为 0700，随机独占文件为 0600，失败或取消时清理本次文件。网络中断、取消或校验失败不会触发重试或更换模型。HTTP 成功头发出后的传输错误表现为响应截断，客户端必须判定为失败。服务密钥仅发往配置的 server，跨源重定向被拒绝。
