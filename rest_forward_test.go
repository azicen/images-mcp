package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestModes covers defaults, CLI/file/env priority, invalid modes/URLs/timeouts,
// key fallbacks, explicit empty overrides and the no-CWD configuration boundary.
func TestModes(t *testing.T) {
	t.Setenv("IMAGES_MCP_MODE", "")
	c := config{}
	if err := c.configureServer(cliOptions{}); err != nil || c.file.Mode != "standalone" || c.file.Server.Listen != ":8080" {
		t.Fatal(c, err)
	}
	t.Setenv("IMAGES_MCP_MODE", "forward")
	t.Setenv("IMAGES_MCP_FORWARD_URL", "https://example.com/prefix")
	t.Setenv("IMAGES_MCP_FORWARD_KEY", "env")
	c = config{}
	if err := c.configureServer(cliOptions{}); err != nil || c.file.Forward.APIKey != "env" || c.file.Forward.Timeout != "15m" {
		t.Fatal(c, err)
	}
	c = config{file: fileConfig{Mode: "server", Server: serverConfig{Listen: ":9000", APIKey: "file"}}}
	opts, err := parseCLI([]string{"-mode", "forward", "-forward-url", "https://cli.example/base/", "-forward-key", "cli", "-forward-timeout", "12m", "-listen", ":8000", "-server-key", "cli-server"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.configureServer(opts); err != nil || c.file.Forward.URL != "https://cli.example/base" || c.file.Forward.APIKey != "cli" || c.file.Server.APIKey != "cli-server" || c.file.Server.Listen != ":8000" {
		t.Fatal(c, err)
	}
	for _, url := range []string{"", "ftp://example.com", "https://a:b@example.com", "https://example.com?q=1", "https://example.com#", "https://example.com/#x"} {
		c = config{file: fileConfig{Mode: "forward", Forward: forwardConfig{URL: url}}}
		opts := cliOptions{set: map[string]bool{"forward-url": true}, forwardURL: url}
		if err := c.configureServer(opts); err == nil {
			t.Fatal(url)
		}
	}
	for _, mode := range []string{"stdio", "http", "bad"} {
		c = config{file: fileConfig{Mode: mode}}
		if err := c.configureServer(cliOptions{}); err == nil {
			t.Fatal(mode)
		}
	}
	c = config{file: fileConfig{Mode: "forward", Forward: forwardConfig{URL: "https://example.com", Timeout: "0s"}}}
	if err := c.configureServer(cliOptions{}); err == nil {
		t.Fatal("timeout")
	}
	t.Setenv("IMAGES_MCP_FORWARD_KEY", "")
	t.Setenv("FORWARD_KEY", "fallback")
	c = config{}
	_ = c.configureServer(cliOptions{})
	if c.file.Forward.APIKey != "fallback" {
		t.Fatal(c)
	}
	t.Setenv("FORWARD_KEY", "")
	t.Setenv("MCP_FORWARD_KEY", "third")
	c = config{}
	_ = c.configureServer(cliOptions{})
	if c.file.Forward.APIKey != "third" {
		t.Fatal(c)
	}
	t.Chdir(t.TempDir())
	_ = os.WriteFile("images-mcp.yaml", []byte("invalid: ["), 0600)
	if _, err := loadFileConfig(""); err != nil {
		t.Fatal(err)
	}
	if _, err := loadFileConfig("images-mcp.yaml"); err == nil {
		t.Fatal("relative config")
	}
	for _, flag := range []string{"-http", "-c"} {
		if _, err := parseCLI([]string{flag, "value"}, io.Discard); err == nil {
			t.Fatal(flag)
		}
	}
}

type formPart struct {
	name, filename, value string
	data                  []byte
}

func formRequest(t *testing.T, parts []formPart) *http.Request {
	t.Helper()
	var b bytes.Buffer
	mw := multipart.NewWriter(&b)
	for _, part := range parts {
		if part.filename != "" {
			p, err := mw.CreateFormFile(part.name, part.filename)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = p.Write(part.data)
		} else {
			_ = mw.WriteField(part.name, part.value)
		}
	}
	_ = mw.Close()
	r := httptest.NewRequest("POST", "/v1/generate", &b)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return r
}

// TestRESTBoundary maps valid JSON and both multipart encodings to the same core,
// and covers auth/method/content/ambiguity/reference/MIME/size rejection branches.
func TestRESTBoundary(t *testing.T) {
	raw, _ := decodeBase64(testPNG)
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": testPNG}}})
	}))
	defer relay.Close()
	c := config{baseURL: relay.URL, apiKey: "local-test", outputFormat: "base64", file: fileConfig{Mode: "server", DefaultModel: "custom", Models: map[string]fileModel{"custom": {API: "images"}}, Server: serverConfig{APIKey: "secret"}}}
	h := newHTTPHandler(&service{config: c, client: relay.Client()})
	for _, tt := range []struct {
		method, path, key, body, ct string
		status                      int
	}{
		{"GET", "/healthz", "", "", "", 200}, {"POST", "/healthz", "", "", "", 405},
		{"GET", "/v1/models", "", "", "", 401}, {"GET", "/v1/models", "wrong", "", "", 401}, {"GET", "/v1/models", "secret", "", "", 200},
		{"POST", "/v1/models", "secret", "", "", 405}, {"GET", "/v1/generate", "secret", "", "", 405},
		{"POST", "/v1/generate", "secret", `{"prompt":"draw"}`, "application/json", 200},
		{"POST", "/v1/generate", "secret", `{"prompt":"draw","api_key":"x"}`, "application/json", 400},
		{"POST", "/v1/generate", "secret", `{"prompt":"draw","input_images":["/etc/passwd"]}`, "application/json", 400},
		{"POST", "/v1/generate", "secret", `{"prompt":"draw","input_images":["part:missing"]}`, "application/json", 400},
		{"POST", "/v1/generate", "secret", `{} {}`, "application/json", 400},
		{"POST", "/v1/generate", "secret", `{}`, "application/json", 400},
		{"POST", "/v1/generate", "secret", `{"prompt":"draw","timeout":"bad"}`, "application/json", 400},
		{"POST", "/v1/generate", "secret", `{"prompt":"draw","model":"unknown"}`, "application/json", 400},
		{"POST", "/v1/generate", "secret", `{"prompt":"draw","input_images":["data:image/png;base64,eA=="]}`, "application/json", 400},
		{"POST", "/v1/generate", "secret", `{}`, "text/plain", 400},
		{"POST", "/v1/generate", "secret", `{}`, ";", 400},
		{"POST", "/v1/generate", "secret", `{}`, "multipart/form-data", 400},
		{"GET", "/mcp", "secret", "", "", 404}, {"GET", "/sse", "secret", "", "", 404}, {"POST", "/upload", "secret", "", "", 404},
	} {
		t.Run(tt.method+tt.path+tt.body+tt.key, func(t *testing.T) {
			r := httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			r.Header.Set("X-API-Key", tt.key)
			r.Header.Set("Content-Type", tt.ct)
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	for _, tt := range []struct {
		name   string
		parts  []formPart
		status int
	}{
		{"request", []formPart{{name: "request", value: `{"prompt":"draw","input_images":["part:file_0"]}`}, {name: "file_0", filename: "a.png", data: raw}}, 200},
		{"fields", []formPart{{name: "prompt", value: "draw"}, {name: "input_images", value: `["part:a"]`}, {name: "a", filename: "a.png", data: raw}}, 200},
		{"mixed", []formPart{{name: "request", value: `{}`}, {name: "prompt", value: "draw"}}, 400},
		{"duplicate", []formPart{{name: "prompt", value: "a"}, {name: "prompt", value: "b"}}, 400},
		{"duplicate file", []formPart{{name: "a", filename: "a.png", data: raw}, {name: "a", filename: "b.png", data: raw}}, 400},
		{"collision", []formPart{{name: "a", value: "x"}, {name: "a", filename: "b.png", data: raw}}, 400},
		{"orphan", []formPart{{name: "prompt", value: "draw"}, {name: "a", filename: "a.png", data: raw}}, 400},
		{"bad MIME", []formPart{{name: "a", filename: "a.png", data: []byte("bad")}}, 400},
		{"bad array", []formPart{{name: "input_images", value: `{}`}}, 400},
		{"unknown", []formPart{{name: "unknown", value: "x"}}, 400},
		{"bad request", []formPart{{name: "request", value: "{"}}, 400},
		{"big field", []formPart{{name: "prompt", value: strings.Repeat("x", (1<<20)+1)}}, 413},
		{"big file", []formPart{{name: "a", filename: "a.png", data: make([]byte, maxImageBytes+1)}}, 413},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := formRequest(t, tt.parts)
			r.Header.Set("Authorization", "Bearer secret")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != tt.status {
				t.Fatalf("%d %s", w.Code, w.Body.String())
			}
		})
	}
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	authorize("", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })).ServeHTTP(w, r)
	if w.Code != 204 {
		t.Fatal(w.Code)
	}
	relay.Close()
	r = httptest.NewRequest("POST", "/v1/generate", strings.NewReader(`{"prompt":"draw"}`))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-API-Key", "secret")
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 500 {
		t.Fatal(w.Code)
	}
}

// TestForwardEndToEnd exercises MCP -> forward -> REST -> mocked relay, including
// ordered local/URL/Data URI references, native images, model proxy and JSON calls.
func TestForwardEndToEnd(t *testing.T) {
	raw, _ := decodeBase64(testPNG)
	path := filepath.Join(t.TempDir(), "cat.png")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/v1/images/edits" {
			mr, err := r.MultipartReader()
			if err != nil {
				t.Error(err)
				return
			}
			var got [][]byte
			for {
				p, err := mr.NextPart()
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Error(err)
					return
				}
				b, _ := io.ReadAll(p)
				if p.FileName() != "" {
					got = append(got, b)
				}
			}
			if len(got) != 3 || !bytes.Equal(got[0], raw) || !bytes.Equal(got[1], raw) {
				t.Errorf("reference order/bytes: %d", len(got))
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": testPNG}}})
	}))
	defer relay.Close()
	cfg := config{baseURL: relay.URL, apiKey: "mock", outputFormat: "base64", file: fileConfig{Mode: "server", DefaultModel: "custom", Models: map[string]fileModel{"custom": {API: "images"}}}}
	host := httptest.NewServer(newHTTPHandler(&service{config: cfg, client: relay.Client()}))
	defer host.Close()
	ctx := context.Background()
	a, b := mcp.NewInMemoryTransports()
	ss, err := newForwardServer(forwardConfig{URL: host.URL, Timeout: "1m"}).Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 2 {
		t.Fatal(list, err)
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_models", Arguments: map[string]any{}})
	if err != nil || result.IsError || !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "custom") {
		t.Fatal(result, err)
	}
	for _, refs := range [][]string{nil, {path, "data:image/png;base64," + testPNG, path}} {
		result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "generate_image", Arguments: map[string]any{"prompt": "draw", "input_images": refs}})
		if err != nil || result.IsError || result.StructuredContent == nil {
			t.Fatal(result, err)
		}
		if len(result.Content) != 2 {
			t.Fatalf("native content: %+v", result.Content)
		}
	}
	if calls != 2 {
		t.Fatal(calls)
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "generate_image", Arguments: map[string]any{"prompt": "draw", "input_images": []string{"/missing/file.png"}}})
	if err != nil || !result.IsError {
		t.Fatal(result, err)
	}
}

// TestForwardFailures covers remote errors/malformed JSON, cancellation, local
// validation, bounded responses and cross-origin redirects without a second call.
func TestForwardFailures(t *testing.T) {
	for _, tt := range []struct {
		status int
		body   string
	}{{401, `{"error":"unauthorized"}`}, {500, `oops`}, {200, `bad json`}} {
		host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tt.status)
			_, _ = w.Write([]byte(tt.body))
		}))
		f := newForwardClient(forwardConfig{URL: host.URL, Timeout: "1m"})
		var out any
		if err := f.call(context.Background(), "GET", "/v1/models", "", nil, &out); err == nil {
			t.Fatal(tt)
		}
		host.Close()
	}
	f := newForwardClient(forwardConfig{URL: "http://127.0.0.1:1", Timeout: "1m"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.call(ctx, "GET", "/v1/models", "", nil, &[]modelRecord{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	for _, refs := range [][]string{make([]string, 17), {"/missing/file"}, {t.TempDir()}} {
		if _, err := f.generate(context.Background(), generateInput{InputImages: refs}); err == nil {
			t.Fatal(refs)
		}
	}
	path := filepath.Join(t.TempDir(), "bad.png")
	_ = os.WriteFile(path, []byte("bad"), 0600)
	if _, err := f.generate(context.Background(), generateInput{InputImages: []string{path}}); err == nil {
		t.Fatal("MIME")
	}
	file, err := os.CreateTemp(t.TempDir(), "large")
	if err != nil {
		t.Fatal(err)
	}
	_ = file.Truncate(maxImageBytes + 1)
	_ = file.Close()
	if _, err := f.generate(context.Background(), generateInput{InputImages: []string{file.Name()}}); err == nil {
		t.Fatal("size")
	}
	var leaked bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer origin.Close()
	f = newForwardClient(forwardConfig{URL: origin.URL, APIKey: "secret", Timeout: "1m"})
	if err := f.call(context.Background(), "GET", "/v1/models", "", nil, &[]modelRecord{}); err == nil || leaked {
		t.Fatal(err, leaked)
	}
	if _, err := imageToolResult(generateOutput{Images: []imageMetadata{{Base64: "invalid"}}}); err == nil {
		t.Fatal("bad base64")
	}
}

// TestMultipartOrder verifies reference ordering follows input_images rather than
// upload order, including repeated references and malformed multipart endings.
func TestMultipartOrder(t *testing.T) {
	raw, _ := decodeBase64(testPNG)
	second := append(append([]byte(nil), raw...), 0)
	r := formRequest(t, []formPart{{name: "b", filename: "b.png", data: second}, {name: "a", filename: "a.png", data: raw}, {name: "request", value: `{"prompt":"edit","input_images":["part:a","part:b","part:a"]}`}})
	in, err := decodeREST(r)
	if err != nil {
		t.Fatal(err)
	}
	images, err := (&service{}).loadInputImages(context.Background(), in)
	if err != nil || !reflect.DeepEqual(images[0].data, raw) || !reflect.DeepEqual(images[1].data, second) || !reflect.DeepEqual(images[2].data, raw) {
		t.Fatal(images, err)
	}
	r = httptest.NewRequest("POST", "/v1/generate", strings.NewReader("--boundary\r\n"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=boundary")
	if _, err := decodeREST(r); err == nil {
		t.Fatal("truncated multipart")
	}
}

// TestForwardTimeout ensures the configured HTTP budget cancels a slow remote.
func TestForwardTimeout(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(time.Second):
		}
	}))
	defer host.Close()
	f := newForwardClient(forwardConfig{URL: host.URL, Timeout: "1ms"})
	if err := f.call(context.Background(), "GET", "/v1/models", "", nil, &[]modelRecord{}); err == nil {
		t.Fatal("timeout ignored")
	}
}
