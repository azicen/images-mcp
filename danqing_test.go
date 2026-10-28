package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aX1sAAAAASUVORK5CYII="

// TestRoute covers both families, editing presence, optional GPT fields, and invalid prompt/model/options.
func TestRoute(t *testing.T) {
	tests := []struct {
		model, input, prompt, size, want string
		bad                              bool
	}{
		{model: "gemini-3.1-flash-image", prompt: "draw", want: "/chat/completions"},
		{model: "Gemini-test", input: "image", prompt: "edit", want: "/chat/completions"},
		{model: "gpt-image-2.5", prompt: "draw", want: "/images/generations"},
		{model: "gpt-image-2.5-flare", input: "image", prompt: "edit", want: "/images/edits"},
		{model: "gpt-image-2.5-sunburst", prompt: "draw", size: "1024x1024", want: "/images/generations"},
		{model: "unknown", prompt: "draw", bad: true}, {model: "gpt-image", prompt: " ", bad: true},
		{model: "gemini", prompt: "draw", size: "auto", bad: true},
	}
	for _, tt := range tests {
		t.Run(tt.model+tt.input+tt.size, func(t *testing.T) {
			got, err := route(generateInput{Model: tt.model, InputImage: tt.input, Prompt: tt.prompt, Size: tt.size})
			if (err != nil) != tt.bad || got != tt.want {
				t.Fatalf("route = %q, %v", got, err)
			}
		})
	}
}

// TestParseText covers Markdown destinations/titles, data URI, URL, raw and fenced base64, and non-image text.
func TestParseText(t *testing.T) {
	uri := "data:image/png;base64," + testPNG
	tests := []struct {
		name, text string
		want       []string
	}{
		{"markdown", "Here ![image](https://example.com/a.png)", []string{"https://example.com/a.png"}},
		{"title", `![a](<https://example.com/a.png> "title")`, []string{"https://example.com/a.png"}},
		{"data markdown", "![a](" + uri + ")", []string{uri}}, {"data", uri, []string{uri}},
		{"url", "https://example.com/a.png", []string{"https://example.com/a.png"}},
		{"raw", testPNG, []string{testPNG}}, {"fenced", "```base64\n" + testPNG + "\n```", []string{testPNG}},
		{"text", "no image here", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseText(tt.text); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// TestParseResponse covers string and structured chat, duplicate images, image API fields, and invalid/empty/error responses.
func TestParseResponse(t *testing.T) {
	tests := []struct {
		name, body, endpoint string
		count                int
		bad                  bool
	}{
		{"chat", `{"choices":[{"message":{"content":"![x](https://example.com/x.png)"}}]}`, "/chat/completions", 1, false},
		{"parts", `{"choices":[{"message":{"content":[{"type":"image_url","image_url":{"url":"https://example.com/x.png"}},{"type":"image_url","image_url":"https://example.com/x.png"}]}}]}`, "/chat/completions", 1, false},
		{"images", `{"choices":[{"message":{"images":[{"image_url":{"url":"https://example.com/x.png"}}]}}]}`, "/chat/completions", 1, false},
		{"generation", `{"data":[{"b64_json":"` + testPNG + `"},{"url":"https://example.com/x.png"}]}`, "/images/generations", 2, false},
		{"empty", `{}`, "/chat/completions", 0, true}, {"invalid", `{`, "/images/edits", 0, true},
		{"error", `{"error":{"message":"secret"}}`, "/images/generations", 0, true},
		{"bad content", `{"choices":[{"message":{"content":42}}]}`, "/chat/completions", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseResponse([]byte(tt.body), tt.endpoint)
			if (err != nil) != tt.bad || len(got) != tt.count {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
	refs := make([]map[string]string, 17)
	for i := range refs {
		refs[i] = map[string]string{"url": "https://example.com/" + strings.Repeat("x", i+1)}
	}
	body, _ := json.Marshal(map[string]any{"data": refs})
	if _, err := parseResponse(body, "/images/generations"); err == nil {
		t.Fatal("accepted too many images")
	}
}

// TestPayloads verifies all routing paths, JSON content shapes, optional fields, multipart image bytes, and authentication.
func TestPayloads(t *testing.T) {
	for _, model := range []string{"gemini-test", "gpt-image-2.5"} {
		for _, edit := range []bool{false, true} {
			t.Run(model+map[bool]string{true: "edit", false: "generate"}[edit], func(t *testing.T) {
				s := &service{config: config{baseURL: "https://relay.example/v1", apiKey: "key"}, client: http.DefaultClient}
				in := generateInput{Model: model, Prompt: "draw"}
				if edit {
					in.InputImage = "data:image/png;base64," + testPNG
				}
				if strings.HasPrefix(model, "gpt") {
					in.Size = "1024x1024"
					in.Quality = "high"
				}
				req, endpoint, err := s.request(context.Background(), in)
				if err != nil {
					t.Fatal(err)
				}
				if req.Method != "POST" || req.URL.Path != "/v1"+endpoint || req.Header.Get("Authorization") != "Bearer key" {
					t.Fatal("invalid request", req)
				}
				if endpoint == "/images/edits" {
					if err := req.ParseMultipartForm(1 << 20); err != nil {
						t.Fatal(err)
					}
					defer req.MultipartForm.RemoveAll()
					for key, want := range map[string]string{"model": model, "prompt": "draw", "size": "1024x1024", "quality": "high"} {
						if req.FormValue(key) != want {
							t.Errorf("%s mismatch", key)
						}
					}
					f, h, err := req.FormFile("image")
					if err != nil {
						t.Fatal(err)
					}
					defer f.Close()
					b, err := readLimited(f, maxImageBytes)
					if err != nil {
						t.Fatal(err)
					}
					want, _ := base64.StdEncoding.DecodeString(testPNG)
					if !bytes.Equal(b, want) || h.Header.Get("Content-Type") != "image/png" {
						t.Fatal("bad multipart image")
					}
				} else {
					var payload map[string]any
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					if payload["model"] != model {
						t.Fatal(payload)
					}
					if strings.HasPrefix(model, "gemini") {
						msg := payload["messages"].([]any)[0].(map[string]any)
						if msg["role"] != "user" {
							t.Fatal(msg)
						}
						if edit {
							parts := msg["content"].([]any)
							if len(parts) != 2 || parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != in.InputImage {
								t.Fatal(parts)
							}
						} else if msg["content"] != "draw" {
							t.Fatal(msg)
						}
					} else if payload["prompt"] != "draw" || payload["size"] != in.Size || payload["quality"] != "high" {
						t.Fatal(payload)
					}
				}
			})
		}
	}
}

// TestImageStorage covers all encodings, local input, downloads without credentials, invalid bytes, unique paths, and exact output metadata.
func TestImageStorage(t *testing.T) {
	raw, _ := base64.StdEncoding.DecodeString(testPNG)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("credential leaked")
		}
		_, _ = w.Write(raw)
	}))
	defer server.Close()
	dir := t.TempDir()
	local := filepath.Join(dir, "input.png")
	if err := os.WriteFile(local, raw, 0600); err != nil {
		t.Fatal(err)
	}
	s := &service{config: config{outputDir: filepath.Join(dir, "output")}, client: server.Client()}
	for _, ref := range []string{testPNG, strings.TrimRight(testPNG, "="), base64.RawURLEncoding.EncodeToString(raw), "data:image/jpeg;base64," + testPNG, server.URL, local} {
		b, mimeType, err := s.loadImage(context.Background(), ref, true)
		if err != nil || mimeType != "image/png" || !bytes.Equal(b, raw) {
			t.Fatalf("load: %s %v", mimeType, err)
		}
	}
	first, err := s.saveImage(context.Background(), testPNG)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.saveImage(context.Background(), testPNG)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path == second.Path || !filepath.IsAbs(first.Path) || first.Bytes != len(raw) || first.MIMEType != "image/png" {
		t.Fatal(first, second)
	}
	saved, err := os.ReadFile(first.Path)
	if err != nil || !bytes.Equal(saved, raw) {
		t.Fatal("saved bytes differ", err)
	}
	for _, ref := range []string{"invalid!", "data:image/png,hello", base64.StdEncoding.EncodeToString([]byte("hello")), "data:text/plain;base64," + testPNG} {
		if _, _, err := s.loadImage(context.Background(), ref, false); err == nil {
			t.Errorf("accepted %q", ref)
		}
	}
	if _, err := readLimited(strings.NewReader("12345"), 4); err == nil {
		t.Fatal("missing size limit")
	}
}

// TestGenerate covers successful persistence, upstream status errors, cancellation, and rollback on a later invalid image.
func TestGenerate(t *testing.T) {
	for _, mode := range []string{"success", "status", "rollback", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer key" {
					t.Error("missing relay auth")
				}
				if mode == "status" {
					w.WriteHeader(401)
					return
				}
				images := []map[string]string{{"b64_json": testPNG}}
				if mode == "rollback" {
					images = append(images, map[string]string{"b64_json": "bad"})
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": images})
			}))
			defer server.Close()
			dir := t.TempDir()
			s := &service{config: config{baseURL: server.URL + "/v1", apiKey: "key", outputDir: dir}, client: server.Client()}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			out, err := s.generate(ctx, generateInput{Model: "gpt-image-2.5", Prompt: "draw"})
			if (err != nil) != (mode != "success") {
				t.Fatalf("output=%v err=%v", out, err)
			}
			files, _ := os.ReadDir(dir)
			if mode == "success" {
				if len(files) != 1 || len(out.Images) != 1 {
					t.Fatal(out, files)
				}
			} else if len(files) != 0 {
				t.Fatal("left partial files")
			}
		})
	}
}

// TestConfig verifies startup snapshots and output paths, then exercises resolution.
// Paths: absent/present family fields and absent/present request fields resolve independently;
// valid credentials succeed, missing/invalid final URL or token fails without exposing values.
func TestConfig(t *testing.T) {
	for _, name := range []string{"OPENAI_BASE_URL", "OPENAI_API_KEY", "GEMINI_BASE_URL", "GEMINI_API_KEY", "GPT_IMAGE_BASE_URL", "GPT_IMAGE_API_KEY", "IMAGE_OUTPUT_DIR"} {
		t.Setenv(name, "")
	}
	tests := []struct {
		name, model, familyURL, familyKey, requestURL, requestKey, wantURL, wantKey string
	}{
		{name: "default", model: "gemini-test", wantURL: "https://default.example/v1", wantKey: "default-key"},
		{name: "gemini", model: "Gemini-test", familyURL: "https://family.example/prefix/", familyKey: "family-key", wantURL: "https://family.example/prefix/v1", wantKey: "family-key"},
		{name: "gpt", model: "gpt-image-test", familyURL: "https://family.example/v1/", familyKey: "family-key", wantURL: "https://family.example/v1", wantKey: "family-key"},
		{name: "request", model: "gpt-image", familyURL: "https://family.example", familyKey: "family-key", requestURL: "https://request.example", requestKey: "request-key", wantURL: "https://request.example/v1", wantKey: "request-key"},
		{name: "family URL only", model: "gemini", familyURL: "https://family.example", wantURL: "https://family.example/v1", wantKey: "default-key"},
		{name: "family key only", model: "gpt-image", familyKey: "family-key", wantURL: "https://default.example/v1", wantKey: "family-key"},
		{name: "request URL only", model: "gemini", familyKey: "family-key", requestURL: "https://request.example", wantURL: "https://request.example/v1", wantKey: "family-key"},
		{name: "request key only", model: "gpt-image", familyURL: "https://family.example", requestKey: "request-key", wantURL: "https://family.example/v1", wantKey: "request-key"},
		{name: "blank overrides", model: "gemini", familyURL: "  ", familyKey: "  ", requestURL: "  ", requestKey: "  ", wantURL: "https://default.example/v1", wantKey: "default-key"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("OPENAI_BASE_URL", " https://default.example/ ")
			t.Setenv("OPENAI_API_KEY", " default-key ")
			prefix := "GEMINI"
			if strings.HasPrefix(tt.model, "gpt-image") {
				prefix = "GPT_IMAGE"
			}
			t.Setenv(prefix+"_BASE_URL", tt.familyURL)
			t.Setenv(prefix+"_API_KEY", tt.familyKey)
			c, err := loadConfig()
			if err != nil || !filepath.IsAbs(c.outputDir) {
				t.Fatal("load configuration failed")
			}
			req, _, err := (&service{config: c}).request(context.Background(), generateInput{Model: tt.model, Prompt: "draw", BaseURL: tt.requestURL, APIKey: tt.requestKey})
			if err != nil {
				t.Fatal(err)
			}
			if req.URL.String() != tt.wantURL+map[bool]string{true: "/chat/completions", false: "/images/generations"}[strings.HasPrefix(strings.ToLower(tt.model), "gemini")] || req.Header.Get("Authorization") != "Bearer "+tt.wantKey {
				t.Fatal("incorrect resolved credentials")
			}
		})
	}
	for _, base := range []string{"", "ftp://host", "https://user:pass@host", "https://host?q=1", "https://host#fragment", "https://host?", "https://host#", "://bad"} {
		if _, err := (config{baseURL: base, apiKey: "key"}).resolveCredentials(generateInput{}); err == nil {
			t.Errorf("accepted invalid base URL %q", base)
		}
	}
	for _, key := range []string{"", "bad\nkey", "bad key", "bad\x7fkey"} {
		if _, err := (config{baseURL: "https://host", apiKey: key}).resolveCredentials(generateInput{}); err == nil {
			t.Fatal("accepted invalid API key")
		}
	}
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.resolveCredentials(generateInput{BaseURL: "https://request.example", APIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	for _, model := range []string{"gemini", "gpt-image"} {
		familyOnly := config{gemini: credentials{baseURL: "https://family.example", apiKey: "key"}, gptImage: credentials{baseURL: "https://family.example", apiKey: "key"}}
		if _, err := familyOnly.resolveCredentials(generateInput{Model: model}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (config{baseURL: "https://host", apiKey: "key"}).resolveCredentials(generateInput{BaseURL: "invalid"}); err == nil {
		t.Fatal("invalid override fell back")
	}
}

// TestOverrideDownload checks the full generation/save path: request credentials go
// only to the relay, including when the image is downloaded from the same origin.
func TestOverrideDownload(t *testing.T) {
	raw, _ := base64.StdEncoding.DecodeString(testPNG)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/image" {
			if r.Header.Get("Authorization") != "" {
				t.Error("download leaked credential")
			}
			_, _ = w.Write(raw)
			return
		}
		if r.Header.Get("Authorization") != "Bearer request-key" {
			t.Error("incorrect relay credential")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"url": "http://" + r.Host + "/image"}}})
	}))
	defer server.Close()
	s := &service{config: config{outputDir: t.TempDir()}, client: server.Client()}
	out, err := s.generate(context.Background(), generateInput{Model: "gpt-image", Prompt: "draw", BaseURL: server.URL, APIKey: "request-key"})
	if err != nil || len(out.Images) != 1 {
		t.Fatalf("generation failed: %v", err)
	}
	encoded, _ := json.Marshal(out)
	if strings.Contains(string(encoded), "request-key") {
		t.Fatal("metadata leaked credential")
	}
}

// TestMCP verifies SDK registration, inferred schema, structured results, and tool error conversion over an in-memory session.
func TestMCP(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": testPNG}}})
	}))
	defer relay.Close()
	s := &service{config: config{baseURL: relay.URL, apiKey: "key", outputDir: t.TempDir()}, client: relay.Client()}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a, b := mcp.NewInMemoryTransports()
	session, err := newServer(s).Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil)
	cs, err := client.Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 1 || list.Tools[0].Name != "generate_image" {
		t.Fatal(list, err)
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "generate_image", Arguments: map[string]any{"model": "gpt-image-2.5", "prompt": "draw"}})
	if err != nil || result.IsError || result.StructuredContent == nil || len(result.Content) == 0 {
		t.Fatal(result, err)
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "generate_image", Arguments: map[string]any{"model": "unknown", "prompt": "draw"}})
	if err != nil || !result.IsError {
		t.Fatal(result, err)
	}
}
