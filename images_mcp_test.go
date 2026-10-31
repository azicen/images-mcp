package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

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
			got, err := route(generateInput{Model: tt.model, InputImages: []string{tt.input}, Prompt: tt.prompt, Size: tt.size})
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
					in.InputImages = []string{"data:image/png;base64," + testPNG}
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
							if len(parts) != 2 || parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != in.InputImages[0] {
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
	if err != nil || len(list.Tools) != 2 || list.Tools[0].Name != "generate_image" || list.Tools[1].Name != "list_models" {
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

// TestFlags covers absolute paths, equals syntax, last-value precedence, help,
// and rejected unknown flags, missing/empty values, and positional arguments.
func TestFlags(t *testing.T) {
	for _, tt := range []struct {
		args []string
		want string
		bad  bool
	}{
		{nil, "", false}, {[]string{"-config", "/a.yaml"}, "/a.yaml", false},
		{[]string{"-config=/a.json"}, "/a.json", false}, {[]string{"-config", "/a", "-config", "/b"}, "/b", false},
		{[]string{"-config"}, "", true}, {[]string{"-c="}, "", true},
		{[]string{"-unknown"}, "", true}, {[]string{"extra"}, "", true}, {[]string{"-h"}, "", true},
	} {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			got, err := parseFlags(tt.args, &bytes.Buffer{})
			if (err != nil) != tt.bad || got != tt.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

// TestFileLoading covers each extension, the complete schema, empty files,
// malformed/type-invalid files, multiple documents, unsupported extensions,
// missing explicit files, oversized input, and directories. Errors omit secrets.
func TestFileLoading(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		bad        bool
	}{
		{"config.yaml", "default:\n  base_url: https://default.example\n  api_key: default-key\nfamilies:\n  gemini:\n    api_key: family-key\nmodels:\n  gemini-test:\n    api_key: model-key\noutput_dir: output\n", false},
		{"config.yml", "default: {api_key: key}", false},
		{"config.json", `{"default":{"base_url":"https://default.example","api_key":"default-key"},"families":{"gemini":{"api_key":"family-key"}},"models":{"gemini-test":{"api_key":"model-key"}},"output_dir":"output"}`, false},
		{"empty.yaml", "", false}, {"empty.json", "", true},
		{"bad.yaml", "api_key: [secret", true}, {"bad.json", `{"secret":`, true},
		{"type.json", `{"default":42}`, true}, {"type.yaml", "default: []", true},
		{"multi.yaml", "{}\n---\n{}", true}, {"duplicate.yaml", "output_dir: a\noutput_dir: b", true},
		{"config.txt", "{}", true}, {"large.yaml", strings.Repeat(" ", (1<<20)+1), true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tt.name)
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := loadFileConfig(path)
			if (err != nil) != tt.bad {
				t.Fatalf("load: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "secret") {
				t.Fatal("error exposed content")
			}
			if tt.name == "config.yaml" || tt.name == "config.json" {
				if c.Default.APIKey != "default-key" || c.Default.BaseURL != "https://default.example" || c.Families["gemini"].APIKey != "family-key" || c.Models["gemini-test"].APIKey != "model-key" || c.OutputDir != "output" {
					t.Fatalf("incomplete decode: %+v", c)
				}
			}
		})
	}
	if _, err := loadFileConfig(filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Fatal("accepted missing explicit file")
	}
	if _, err := loadFileConfig(t.TempDir()); err == nil {
		t.Fatal("accepted directory")
	}
}

// TestDiscovery paths: absent files permit env-only startup; first existing file
// wins in YAML/YML/JSON order; invalid first file fails without fallback.

func TestFileResolution(t *testing.T) {
	for _, family := range []string{"gemini", "gpt-image"} {
		t.Run(family, func(t *testing.T) {
			model := family + "-test"
			levels := []fileCredentials{}
			for _, name := range []string{"request", "model", "file-family", "env-family", "file-default", "env-default"} {
				levels = append(levels, fileCredentials{BaseURL: "https://" + name + ".example", APIKey: name})
			}
			resolve := func() (credentials, error) {
				c := config{baseURL: levels[5].BaseURL, apiKey: levels[5].APIKey,
					file: fileConfig{Default: levels[4], Families: map[string]fileCredentials{family: levels[2]}, Models: map[string]fileModel{model: {BaseURL: levels[1].BaseURL, APIKey: levels[1].APIKey}}},
				}
				env := credentials{baseURL: levels[3].BaseURL, apiKey: levels[3].APIKey}
				if family == "gemini" {
					c.gemini = env
				} else {
					c.gptImage = env
				}
				return c.resolveCredentials(generateInput{Model: model, BaseURL: levels[0].BaseURL, APIKey: levels[0].APIKey})
			}
			for i := range levels {
				got, err := resolve()
				if err != nil || got.baseURL != levels[i].BaseURL+"/v1" || got.apiKey != levels[i].APIKey {
					t.Fatalf("level %d: %+v %v", i, got, err)
				}
				if i+1 < len(levels) {
					levels[i].BaseURL = " \t "
					got, err = resolve()
					if err != nil || got.baseURL != levels[i+1].BaseURL+"/v1" || got.apiKey != levels[i].APIKey {
						t.Fatalf("partial level %d: %+v %v", i, got, err)
					}
					levels[i].BaseURL = "https://url-only.example"
					levels[i].APIKey = " "
					got, err = resolve()
					if err != nil || got.baseURL != "https://url-only.example/v1" || got.apiKey != levels[i+1].APIKey {
						t.Fatalf("key fallback level %d: %+v %v", i, got, err)
					}
					levels[i].BaseURL = " "
				}
			}
		})
	}
	c := config{baseURL: "https://default.example", apiKey: "default", file: fileConfig{Models: map[string]fileModel{"gemini-test": {BaseURL: "invalid", APIKey: "model"}}}}
	if _, err := c.resolveCredentials(generateInput{Model: "gemini-test"}); err == nil {
		t.Fatal("invalid model override fell back")
	}
	got, err := c.resolveCredentials(generateInput{Model: "Gemini-test"})
	if err != nil || got.apiKey != "default" {
		t.Fatal("model matching was not exact", err)
	}
	c.file.Models["gemini-test"] = fileModel{APIKey: "bad key"}
	if _, err := c.resolveCredentials(generateInput{Model: "gemini-test"}); err == nil {
		t.Fatal("invalid model key fell back")
	}
}

// TestOutputDirectory covers file > env > default, relative paths resolved from
// working directory, absolute paths, and startup snapshots of file/env values.
func TestOutputDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	for _, tt := range []struct{ file, env, want string }{
		{"", "", os.TempDir()}, {"", "env-images", "env-images"}, {"file-images", "env-images", "file-images"},
		{t.TempDir(), "env-images", ""},
	} {
		t.Setenv("IMAGE_OUTPUT_DIR", tt.env)
		b, _ := json.Marshal(fileConfig{OutputDir: tt.file})
		if err := os.WriteFile("custom.json", b, 0600); err != nil {
			t.Fatal(err)
		}
		c, err := loadConfig(filepath.Join(mustGetwd(t), "custom.json"))
		if err != nil {
			t.Fatal(err)
		}
		want := tt.want
		if want == "" {
			want = tt.file
		}
		want, err = filepath.Abs(want)
		if err != nil {
			t.Fatal(err)
		}
		if c.outputDir != want {
			t.Fatalf("got %s want %s", c.outputDir, want)
		}
		t.Setenv("IMAGE_OUTPUT_DIR", "changed")
		if c.outputDir != want {
			t.Fatal("config changed after startup")
		}
	}
}

// TestConfigSnapshot verifies the load-to-request path: file model fields and
// environment family fields combine, then remain fixed after their sources change.
func TestConfigSnapshot(t *testing.T) {
	t.Setenv("GEMINI_BASE_URL", "https://env-family.example")
	t.Setenv("GEMINI_API_KEY", "env-family-key")
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("models:\n  custom-model:\n    api: CHAT\n    api_key: file-model-key\n"), 0600); err != nil {
		t.Fatal(err)
	}
	c, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEMINI_BASE_URL", "https://changed.example")
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	req, _, err := (&service{config: c}).request(context.Background(), generateInput{Model: "custom-model", Prompt: "draw"})
	if err != nil {
		t.Fatal(err)
	}
	if req.URL.String() != "https://env-family.example/v1/chat/completions" || req.Header.Get("Authorization") != "Bearer file-model-key" {
		t.Fatal("startup snapshot was not used")
	}
}

// TestExplicitAPIRouting covers each alias/value in config and tool inputs,
// both edit branches, case folding, prefix override, and family credentials.
// All successful requests must use the selected endpoint and family key.
func TestExplicitAPIRouting(t *testing.T) {
	for _, field := range []string{"api", "api_type"} {
		for _, value := range []string{"gemini", "ChAt", "gpt", "GPT-IMAGE", "images"} {
			for _, source := range []string{"model", "tool"} {
				for _, edit := range []bool{false, true} {
					t.Run(field+"/"+value+"/"+source+map[bool]string{true: "/edit", false: "/generate"}[edit], func(t *testing.T) {
						chat := strings.EqualFold(value, "gemini") || strings.EqualFold(value, "chat")
						family, endpoint := "gpt-image", "/images/generations"
						if chat {
							family, endpoint = "gemini", "/chat/completions"
						} else if edit {
							endpoint = "/images/edits"
						}
						c := config{file: fileConfig{Families: map[string]fileCredentials{
							family: {BaseURL: "https://family.example", APIKey: family},
						}}}
						in := generateInput{Model: "custom-model", Prompt: "draw"}
						if edit {
							in.InputImages = []string{"data:image/png;base64," + testPNG}
						}
						encoded, err := json.Marshal(map[string]string{field: value})
						if err != nil {
							t.Fatal(err)
						}
						if source == "model" {
							path := filepath.Join(t.TempDir(), "config.json")
							body := []byte(`{"models":{"custom-model":` + string(encoded) + `}}`)
							if err := os.WriteFile(path, body, 0600); err != nil {
								t.Fatal(err)
							}
							file, err := loadFileConfig(path)
							if err != nil {
								t.Fatal(err)
							}
							c.file.Models = file.Models
						} else {
							if err := json.Unmarshal(encoded, &in); err != nil {
								t.Fatal(err)
							}
							// Explicit tool values override both config and a contrary prefix.
							in.Model = "gemini-custom"
							opposite := "images"
							if chat {
								in.Model, opposite = "gpt-image-custom", "chat"
							}
							c.file.Models = map[string]fileModel{in.Model: {API: opposite}}
						}
						req, got, err := (&service{config: c}).request(context.Background(), in)
						if err != nil {
							t.Fatal(err)
						}
						defer req.Body.Close()
						if got != endpoint || req.URL.Path != "/v1"+endpoint || req.Header.Get("Authorization") != "Bearer "+family {
							t.Fatalf("route %q, URL %s, authorization %q", got, req.URL, req.Header.Get("Authorization"))
						}
					})
				}
			}
		}
	}
}

// TestInvalidAPI covers invalid aliases at startup and request time, including
// valid-prefix models that must not silently fall back, and conflicting aliases.
func TestInvalidAPI(t *testing.T) {
	for _, field := range []string{"api", "api_type"} {
		for _, ext := range []string{"json", "yaml"} {
			t.Run(field+"/"+ext, func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "config."+ext)
				body := `{"models":{"gemini-test":{"` + field + `":"invalid"}}}`
				if ext == "yaml" {
					body = "models:\n  gemini-test:\n    " + field + ": invalid\n"
				}
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "invalid "+field) {
					t.Fatalf("startup error = %v", err)
				}
			})
		}
		in := generateInput{Model: "gemini-test", Prompt: "draw"}
		if field == "api" {
			in.API = "invalid"
		} else {
			in.APIType = "invalid"
		}
		if _, err := route(in); err == nil || !strings.Contains(err.Error(), "invalid "+field) {
			t.Fatalf("tool error = %v", err)
		}
	}
	if _, err := route(generateInput{Model: "custom", Prompt: "draw", API: "chat", APIType: "images"}); err == nil {
		t.Fatal("accepted conflicting aliases")
	}
	if got, err := route(generateInput{Model: "custom", Prompt: "draw", API: "gemini", APIType: "CHAT"}); err != nil || got != "/chat/completions" {
		t.Fatalf("equivalent aliases = %q, %v", got, err)
	}
}

// TestUnsetOutputDirectory covers absent config and an unset environment;
// the empty settings branch and configured overrides are in TestOutputDirectory.
func TestUnsetOutputDirectory(t *testing.T) {
	t.Chdir(t.TempDir())
	previous, present := os.LookupEnv("IMAGE_OUTPUT_DIR")
	if err := os.Unsetenv("IMAGE_OUTPUT_DIR"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if present {
			_ = os.Setenv("IMAGE_OUTPUT_DIR", previous)
		} else {
			_ = os.Unsetenv("IMAGE_OUTPUT_DIR")
		}
	})
	c, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want, err := filepath.Abs(os.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if c.outputDir != want {
		t.Fatalf("output directory = %q, want %q", c.outputDir, want)
	}
}

// TestModelDiscovery paths: configured/empty catalogs, explicit/prefix routes,
// default/nondefault records, present/absent notes, and omitted/empty/explicit
// model arguments. MCP assertions cover schema optionality and sorted guidance;
// relay assertions verify the resolved model reaches both payload and result.
func TestModelDiscovery(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if payload.Model != "alpha" && payload.Model != "gpt-image-zeta" {
			t.Errorf("unexpected model %q", payload.Model)
		}
		if r.URL.Path != "/v1/images/generations" {
			t.Errorf("unexpected route %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": testPNG}}})
	}))
	defer relay.Close()
	c := config{baseURL: relay.URL, apiKey: "secret-key", outputDir: t.TempDir(), file: fileConfig{DefaultModel: "alpha", Models: map[string]fileModel{
		"gpt-image-zeta": {Description: "Last model"},
		"gemini-middle":  {Description: "Chat model"},
		"alpha":          {APIType: "images", Description: "First model", Notes: "Detailed guidelines.\nUse for editing."},
	}}}
	ctx := context.Background()
	a, b := mcp.NewInMemoryTransports()
	ss, err := newServer(&service{config: c, client: relay.Client()}).Connect(ctx, a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "1"}, nil).Connect(ctx, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var generation *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == "generate_image" {
			generation = tool
		}
	}
	if generation == nil {
		t.Fatal("generate_image missing")
	}
	wantDescription := "Generate or edit images through a unified relay. Output follows the global output_format configuration. Supply input_images for up to 16 ordered images: first is the primary image to edit, others are style/context references. Inputs: PNG/JPEG/WebP, 32 MiB each, 64 MiB total.\nDefault model: alpha.\nConfigured models (alphabetical):\n- alpha: First model\n- gemini-middle: Chat model\n- gpt-image-zeta: Last model\nCall list_models for complete details and guidelines."
	if generation.Description != wantDescription {
		t.Fatalf("description = %q", generation.Description)
	}
	for i := 0; i < 20; i++ {
		if c.generationDescription() != wantDescription {
			t.Fatal("unstable description")
		}
	}
	raw, err := json.Marshal(generation.InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(schema.Required, []string{"prompt"}) {
		t.Fatalf("required = %v", schema.Required)
	}
	result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_models", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatal(result, err)
	}
	content := result.Content[0].(*mcp.TextContent).Text
	var records []modelRecord
	if err := json.Unmarshal([]byte(content), &records); err != nil {
		t.Fatal(err)
	}
	want := []modelRecord{
		{ID: "alpha", Description: "First model", Notes: c.file.Models["alpha"].Notes, SupportsEdit: true, IsDefault: true},
		{ID: "gemini-middle", Description: "Chat model", SupportsEdit: true},
		{ID: "gpt-image-zeta", Description: "Last model", SupportsEdit: true},
	}
	if !reflect.DeepEqual(records, want) {
		t.Fatalf("records = %+v", records)
	}
	if strings.Count(content, `"notes"`) != 1 || strings.Contains(content, "secret-key") || strings.Contains(content, `"api"`) || strings.Contains(content, `"api_type"`) {
		t.Fatalf("unexpected fields: %s", content)
	}
	for _, args := range []map[string]any{{"prompt": "draw"}, {"prompt": "draw", "model": ""}, {"prompt": "draw", "model": "gpt-image-zeta"}} {
		result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "generate_image", Arguments: args})
		if err != nil || result.IsError {
			t.Fatal(result, err)
		}
		raw, _ := json.Marshal(result.StructuredContent)
		var out generateOutput
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		expected := "alpha"
		if args["model"] == "gpt-image-zeta" {
			expected = "gpt-image-zeta"
		}
		if out.Model != expected || len(out.Images) != 1 {
			t.Fatalf("output = %+v", out)
		}
	}
	result, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "generate_image", Arguments: map[string]any{"prompt": "draw", "model": "typo"}})
	if err != nil || !result.IsError {
		t.Fatal(result, err)
	}
	if msg := result.Content[0].(*mcp.TextContent).Text; !strings.Contains(msg, "alpha, gemini-middle, gpt-image-zeta") {
		t.Fatal(msg)
	}
	empty, err := (config{}).listModels()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(empty)
	if string(raw) != "[]" {
		t.Fatalf("empty catalog = %s", raw)
	}
	if _, err := (&service{}).generate(ctx, generateInput{Prompt: "draw", API: "images"}); err == nil || !strings.Contains(err.Error(), "default_model") {
		t.Fatalf("missing default error = %v", err)
	}
}

// TestModelMetadataConfig paths: YAML/JSON preserve short and detailed model
// text and default selection; unroutable custom entries fail with API guidance.
func TestModelMetadataConfig(t *testing.T) {
	for _, tt := range []struct{ name, body string }{
		{"config.yaml", "default_model: custom\nmodels:\n  custom:\n    api: chat\n    description: Short summary\n    notes: |\n      First line\n      Second line\n"},
		{"config.json", `{"default_model":"custom","models":{"custom":{"api":"chat","description":"Short summary","notes":"First line\nSecond line\n"}}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tt.name)
			if err := os.WriteFile(path, []byte(tt.body), 0600); err != nil {
				t.Fatal(err)
			}
			c, err := loadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			if c.file.DefaultModel != "custom" || c.file.Models["custom"].Description != "Short summary" || c.file.Models["custom"].Notes != "First line\nSecond line\n" {
				t.Fatalf("config = %+v", c.file)
			}
			if err := os.WriteFile(path, []byte(`{"models":{"custom":{}}}`), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadConfig(path); err == nil || !strings.Contains(err.Error(), "api") {
				t.Fatalf("unroutable config error = %v", err)
			}
		})
	}
}

// TestMultiImagePayloads covers single-image and multiple-image arrays.
// Branches: one image -> image; multiple -> image[]; chat -> ordered text/images.
// Distinct image bytes verify ordering, duplicates remain meaningful references.
func TestMultiImagePayloads(t *testing.T) {
	raw, _ := base64.StdEncoding.DecodeString(testPNG)
	second := append(append([]byte(nil), raw...), 1)
	refs := []string{testPNG, base64.StdEncoding.EncodeToString(second)}
	for _, model := range []string{"gpt-image-test", "gemini-test"} {
		for _, mode := range []string{"single-array", "multi", "duplicates"} {
			t.Run(model+"/"+mode, func(t *testing.T) {
				in := generateInput{Model: model, Prompt: "edit"}
				want := [][]byte{raw}
				switch mode {
				case "single-array":
					in.InputImages = refs[:1]
				case "multi":
					in.InputImages = []string{"", refs[0], " ", refs[1]}
					want = append(want, second)
				case "duplicates":
					in.InputImages = []string{refs[0], refs[0]}
					want = append(want, raw)
				}
				s := &service{config: config{baseURL: "https://relay.example", apiKey: "key"}, client: http.DefaultClient}
				req, endpoint, err := s.request(context.Background(), in)
				if err != nil {
					t.Fatal(err)
				}
				defer req.Body.Close()
				if strings.HasPrefix(model, "gpt") {
					if endpoint != "/images/edits" {
						t.Fatal(endpoint)
					}
					if err := req.ParseMultipartForm(1 << 20); err != nil {
						t.Fatal(err)
					}
					defer req.MultipartForm.RemoveAll()
					field := "image"
					if len(want) > 1 {
						field = "image[]"
					}
					files := req.MultipartForm.File[field]
					if len(files) != len(want) || len(req.MultipartForm.File) != 1 {
						t.Fatalf("files = %+v", req.MultipartForm.File)
					}
					for i, h := range files {
						f, err := h.Open()
						if err != nil {
							t.Fatal(err)
						}
						got, err := readLimited(f, maxImageBytes)
						f.Close()
						if err != nil || !bytes.Equal(got, want[i]) || h.Filename == "" || h.Header.Get("Content-Type") != "image/png" {
							t.Fatalf("part %d: %v", i, err)
						}
					}
				} else {
					var payload struct {
						Messages []struct {
							Role    string `json:"role"`
							Content []struct {
								Type     string `json:"type"`
								Text     string `json:"text"`
								ImageURL struct {
									URL string `json:"url"`
								} `json:"image_url"`
							} `json:"content"`
						} `json:"messages"`
					}
					if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
						t.Fatal(err)
					}
					if endpoint != "/chat/completions" || len(payload.Messages) != 1 {
						t.Fatal(payload)
					}
					msg := payload.Messages[0]
					if msg.Role != "user" || len(msg.Content) != len(want)+1 || msg.Content[0].Type != "text" || msg.Content[0].Text != in.Prompt {
						t.Fatal(msg)
					}
					for i, b := range want {
						part := msg.Content[i+1]
						if part.Type != "image_url" || part.ImageURL.URL != "data:image/png;base64,"+base64.StdEncoding.EncodeToString(b) {
							t.Fatalf("part %d: %+v", i, part)
						}
					}
				}
			})
		}
	}
}

// TestInputImageValidation covers normalization order/empty filtering, 16/17 count
// boundaries, forbidden MIME types, original error indices, and output GIF support.
// Count errors must precede all network I/O, including reference downloads.
func TestInputImageValidation(t *testing.T) {
	s := &service{config: config{baseURL: "https://relay.example", apiKey: "key", outputDir: t.TempDir()}, client: http.DefaultClient}
	in := generateInput{InputImages: []string{"primary", "second", "", " "}}
	sources, err := in.normalizedInputImages()
	if err != nil || !reflect.DeepEqual(sources, []inputImageSource{{ref: "primary", source: "input_images[0]"}, {ref: "second", source: "input_images[1]"}}) {
		t.Fatal(sources, err)
	}
	in = generateInput{Model: "gpt-image", Prompt: "edit", InputImages: make([]string, 16)}
	for i := range in.InputImages {
		in.InputImages[i] = "https://must-not-be-fetched.invalid/image"
	}
	if _, err := in.normalizedInputImages(); err != nil {
		t.Fatal(err)
	}
	in.InputImages = append(in.InputImages, testPNG)
	if _, _, err := s.request(context.Background(), in); err == nil || !strings.Contains(err.Error(), "16") {
		t.Fatal(err)
	}
	gif := base64.StdEncoding.EncodeToString([]byte("GIF89a" + strings.Repeat("\x00", 20)))
	for _, ref := range []string{gif, "data:image/png;base64," + gif, base64.StdEncoding.EncodeToString([]byte("<svg></svg>")), "/missing/reference.png"} {
		in = generateInput{Model: "gemini", Prompt: "edit", InputImages: []string{testPNG, "", ref}}
		if _, _, err := s.request(context.Background(), in); err == nil || !strings.Contains(err.Error(), "input_images[2]") {
			t.Fatal(err)
		}
	}
	if _, _, err := s.request(context.Background(), generateInput{Model: "gpt-image", Prompt: "edit", InputImages: []string{gif}}); err == nil || !strings.Contains(err.Error(), "input_images[0]:") {
		t.Fatal(err)
	}
	if out, err := s.saveImage(context.Background(), gif); err != nil || out.MIMEType != "image/gif" {
		t.Fatal(out, err)
	}
}

// TestInputImageBudget covers exact 64 MiB acceptance and overflow at the third
// reference; both routes reject overflow before issuing a generation request.
// Each individual file is valid PNG input within the unchanged 32 MiB limit.
func TestInputImageBudget(t *testing.T) {
	raw, _ := base64.StdEncoding.DecodeString(testPNG)
	path := filepath.Join(t.TempDir(), "large.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(raw); err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxImageBytes); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	s := &service{config: config{baseURL: "https://relay.example", apiKey: "key"}, client: http.DefaultClient}
	images, err := s.loadInputImages(context.Background(), generateInput{InputImages: []string{path, path}})
	if err != nil || len(images) != 2 {
		t.Fatal(len(images), err)
	}
	images = nil
	for _, model := range []string{"gpt-image", "gemini"} {
		_, _, err := s.request(context.Background(), generateInput{Model: model, Prompt: "edit", InputImages: []string{path, path, testPNG, "/must-not-be-loaded"}})
		if err == nil || !strings.Contains(err.Error(), "input_images[2]") || !strings.Contains(err.Error(), "64 MiB") {
			t.Fatal(err)
		}
	}
}

// TestParseTimeout covers duration and integer-seconds paths, whitespace,
// parse failures, overflow, and the nonpositive branch; only positive values succeed.
func TestParseTimeout(t *testing.T) {
	for _, tt := range []struct {
		input string
		want  time.Duration
	}{
		{"10m", 10 * time.Minute}, {"15m", 15 * time.Minute},
		{"600s", 10 * time.Minute}, {"600", 10 * time.Minute},
		{"1h", time.Hour}, {" 10ms ", 10 * time.Millisecond},
		{"", 0}, {" ", 0}, {"invalid", 0}, {"0", 0}, {"0s", 0},
		{"-1s", 0}, {"-600", 0}, {"999999999999999999999", 0},
	} {
		t.Run(tt.input, func(t *testing.T) {
			got, err := parseTimeout(tt.input)
			if got != tt.want || (err != nil) != (tt.want == 0) {
				t.Fatalf("parseTimeout(%q) = %v, %v; want %v", tt.input, got, err, tt.want)
			}
		})
	}
}

// TestTimeoutPriority removes each winning value in order, exercising all six
// paths. Invalid winners fail without fallback; an omitted model uses default_model.
func TestTimeoutPriority(t *testing.T) {
	t.Setenv("IMAGE_TIMEOUT", "5m")
	c := config{file: fileConfig{Timeout: "4m", DefaultModel: "gpt-image-test",
		Default: fileCredentials{Timeout: "3m"},
		Models:  map[string]fileModel{"gpt-image-test": {Timeout: "2m"}}}}
	in := generateInput{Timeout: "1m"}
	for i := 1; i <= 6; i++ {
		want := time.Duration(i) * time.Minute
		if i == 6 {
			want = 10 * time.Minute
		}
		got, err := c.resolveTimeout(in)
		if err != nil || got != want {
			t.Fatalf("level %d: %v, %v; want %v", i, got, err, want)
		}
		switch i {
		case 1:
			in.Timeout = ""
		case 2:
			c.file.Models = nil
		case 3:
			c.file.Default.Timeout = ""
		case 4:
			c.file.Timeout = ""
		case 5:
			t.Setenv("IMAGE_TIMEOUT", "")
		}
	}
	for _, level := range []string{"request", "model", "default", "top", "env"} {
		t.Run(level, func(t *testing.T) {
			c := config{file: fileConfig{Models: map[string]fileModel{}}}
			in := generateInput{Model: "gpt-image-test"}
			t.Setenv("IMAGE_TIMEOUT", "10m")
			switch level {
			case "request":
				in.Timeout = "bad"
			case "model":
				c.file.Models[in.Model] = fileModel{Timeout: "bad"}
			case "default":
				c.file.Default.Timeout = "bad"
			case "top":
				c.file.Timeout = "bad"
			case "env":
				t.Setenv("IMAGE_TIMEOUT", "bad")
			}
			if _, err := c.resolveTimeout(in); err == nil {
				t.Fatal("invalid winner accepted")
			}
		})
	}
}

// TestTimeoutConfigValidation covers YAML and JSON decoding at each supported
// scope: valid durations start successfully; invalid/nonpositive ones fail startup.
func TestTimeoutConfigValidation(t *testing.T) {
	for _, ext := range []string{".yaml", ".json"} {
		for _, scope := range []string{"top", "default", "model"} {
			for _, value := range []string{"15m", "bad", "0"} {
				t.Run(ext+scope+value, func(t *testing.T) {
					file := fileConfig{Models: map[string]fileModel{"gpt-image-test": {}}}
					switch scope {
					case "top":
						file.Timeout = value
					case "default":
						file.Default.Timeout = value
					case "model":
						file.Models["gpt-image-test"] = fileModel{Timeout: value}
					}
					// JSON is also valid YAML, so exercise both decoders with identical content.
					body, err := json.Marshal(file)
					if err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(t.TempDir(), "config"+ext)
					if err := os.WriteFile(path, body, 0600); err != nil {
						t.Fatal(err)
					}
					c, err := loadConfig(path)
					if (err != nil) != (value != "15m") {
						t.Fatalf("loadConfig: %v", err)
					}
					if err == nil {
						got, err := c.resolveTimeout(generateInput{Model: "gpt-image-test"})
						if err != nil || got != 15*time.Minute {
							t.Fatalf("decoded timeout: %v, %v", got, err)
						}
					}
				})
			}
		}
	}
}

// TestGenerateTimeout covers cancellation at input download, relay POST, and
// output download. Each stalled HTTP handler waits for cancellation; errors
// must retain context.DeadlineExceeded and leave the output directory empty.
// Invalid request durations fail before I/O; an earlier parent deadline wins.
func TestGenerateTimeout(t *testing.T) {
	for _, stage := range []string{"input", "relay", "output", "parent", "invalid"} {
		t.Run(stage, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if stage == "output" && r.Method == http.MethodPost {
					_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"url": "http://" + r.Host + "/output"}}})
					return
				}
				select {
				case <-r.Context().Done():
				case <-time.After(time.Second):
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			s := service{config: config{baseURL: server.URL, apiKey: "key", outputDir: dir}, client: server.Client()}
			in := generateInput{Model: "gpt-image-test", Prompt: "draw", Timeout: "10ms"}
			if stage == "input" {
				in.InputImages = []string{server.URL + "/input"}
			}
			ctx := context.Background()
			if stage == "parent" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				in.Timeout = "1h"
			}
			if stage == "invalid" {
				in.Timeout = "bad"
			}
			_, err := s.generate(ctx, in)
			if stage == "invalid" {
				if err == nil || !strings.Contains(err.Error(), "request timeout") {
					t.Fatalf("invalid timeout: %v", err)
				}
			} else if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("expected deadline exceeded: %v", err)
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 0 {
				t.Fatalf("unexpected output: %v, %v", entries, err)
			}
		})
	}
}

// TestOutputFormatConfig paths: omitted values use file; top-level > default >
// environment; case normalizes; each nonempty invalid setting fails startup.
// Cases exercise both YAML/JSON and ensure the resolved format is a snapshot.
func TestOutputFormatConfig(t *testing.T) {
	for _, ext := range []string{".yaml", ".json"} {
		for _, tt := range []struct {
			top, def, env, want string
			invalid             bool
		}{
			{"", "", "", "file", false},
			{"", "", "BASE64", "base64", false},
			{"", "BoTh", "base64", "both", false},
			{"FILE", "both", "base64", "file", false},
			{"base64", "", "", "base64", false},
			{"invalid", "", "", "", true},
			{"file", "invalid", "", "", true},
			{"", "", "invalid", "", true},
		} {
			t.Run(ext+"/"+tt.top+"/"+tt.def+"/"+tt.env, func(t *testing.T) {
				t.Setenv("IMAGE_OUTPUT_FORMAT", tt.env)
				path := filepath.Join(t.TempDir(), "config"+ext)
				// JSON is also valid YAML, exercising both file decoders.
				body, err := json.Marshal(fileConfig{OutputFormat: tt.top, Default: fileCredentials{OutputFormat: tt.def}})
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
				c, err := loadConfig(path)
				if tt.invalid {
					if err == nil || !strings.Contains(err.Error(), "output_format") && !strings.Contains(err.Error(), "IMAGE_OUTPUT_FORMAT") {
						t.Fatalf("expected format error, got %v", err)
					}
					return
				}
				if err != nil || c.outputFormat != tt.want {
					t.Fatalf("format %q, error %v; want %q", c.outputFormat, err, tt.want)
				}
				t.Setenv("IMAGE_OUTPUT_FORMAT", "file")
				if c.outputFormat != tt.want {
					t.Fatal("format changed after startup")
				}
			})
		}
	}
}

// TestOutputFormats paths: file writes only, base64 encodes only, both does both.
// Inline and URL results preserve exact bytes; base64 cannot create output_dir.
// MCP calls must carry structured JSON and text plus native images when enabled.
func TestOutputFormats(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(testPNG)
	if err != nil {
		t.Fatal(err)
	}
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(raw)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": testPNG}}})
	}))
	defer relay.Close()
	for _, format := range []string{"file", "base64", "both"} {
		t.Run(format, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "output")
			s := &service{config: config{baseURL: relay.URL, apiKey: "key", outputDir: dir, outputFormat: format}, client: relay.Client()}
			for _, ref := range []string{testPNG, relay.URL + "/image"} {
				img, err := s.saveImage(context.Background(), ref)
				if err != nil {
					t.Fatal(err)
				}
				if img.Bytes != len(raw) || img.MIMEType != "image/png" {
					t.Fatalf("bad metadata: %+v", img)
				}
				if format == "base64" {
					if img.Path != "" {
						t.Fatal("base64 returned a path")
					}
					if _, err := os.Stat(dir); !os.IsNotExist(err) {
						t.Fatalf("base64 created directory: %v", err)
					}
				} else {
					got, err := os.ReadFile(img.Path)
					if err != nil || !bytes.Equal(got, raw) || !filepath.IsAbs(img.Path) {
						t.Fatalf("bad file %q: %v", img.Path, err)
					}
				}
				if format == "file" {
					if img.Base64 != "" {
						t.Fatal("file returned base64")
					}
				} else {
					got, err := base64.StdEncoding.DecodeString(img.Base64)
					if err != nil || !bytes.Equal(got, raw) {
						t.Fatalf("bad base64: %v", err)
					}
				}
			}
			if format == "base64" {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				sentinel := filepath.Join(dir, "keep")
				if err := os.WriteFile(sentinel, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				if _, err := s.saveImage(context.Background(), testPNG); err != nil {
					t.Fatal(err)
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 1 || entries[0].Name() != "keep" {
					t.Fatalf("directory changed: %v %v", entries, err)
				}
				if err := os.Remove(sentinel); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(dir); err != nil {
					t.Fatal(err)
				}
			}
			ctx := context.Background()
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
			result, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "generate_image", Arguments: map[string]any{"model": "gpt-image-2", "prompt": "draw"}})
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError || result.StructuredContent == nil {
				t.Fatalf("invalid result: %+v", result)
			}
			images, texts := 0, 0
			for _, content := range result.Content {
				switch content := content.(type) {
				case *mcp.ImageContent:
					images++
					if content.MIMEType != "image/png" || !bytes.Equal(content.Data, raw) {
						t.Fatal("native image differs")
					}
				case *mcp.TextContent:
					texts++
					var out generateOutput
					if err := json.Unmarshal([]byte(content.Text), &out); err != nil || len(out.Images) != 1 {
						t.Fatalf("invalid text: %v", err)
					}
					if format == "base64" && (out.Images[0].Path != "" || out.Images[0].Base64 != testPNG) {
						t.Fatal("invalid text metadata")
					}
				}
			}
			wantImages := 1
			if format == "file" {
				wantImages = 0
			}
			if images != wantImages || texts != 1 {
				t.Fatalf("images=%d texts=%d", images, texts)
			}
			if format == "base64" {
				if _, err := os.Stat(dir); !os.IsNotExist(err) {
					t.Fatal("MCP base64 wrote output directory")
				}
			}
		})
	}
}

// TestHTTPConfiguration covers file and environment parsing, CLI overrides,
// invalid transport/storage settings, required credentials, and base64/stdio bypass.
func TestHTTPConfiguration(t *testing.T) {
	for _, name := range []string{"MCP_TRANSPORT", "MCP_LISTEN", "MCP_SERVER_KEY", "SERVER_API_KEY", "S3_ENDPOINT", "AWS_ENDPOINT_URL_S3", "S3_BUCKET", "S3_REGION", "AWS_REGION", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY", "AWS_SESSION_TOKEN", "S3_USE_PATH_STYLE", "S3_PUBLIC_URL_PREFIX"} {
		t.Setenv(name, "")
	}
	opts, err := parseCLI([]string{"-mode", "server", "-listen", "127.0.0.1:8081", "-server-key", "cli"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("IMAGES_MCP_MODE", "server")
	t.Setenv("MCP_LISTEN", ":8090")
	t.Setenv("SERVER_API_KEY", "fallback")
	c := config{outputFormat: "file"}
	if err := c.configureServer(cliOptions{}); err != nil || c.file.Server.Listen != ":8090" || c.file.Server.APIKey != "fallback" {
		t.Fatal(c, err)
	}
	if _, err := newObjectStorage(c); err == nil {
		t.Fatal("missing S3 accepted")
	}
	if err := c.configureServer(opts); err != nil || c.file.Server.Listen != "127.0.0.1:8081" || c.file.Server.APIKey != "cli" {
		t.Fatal(c, err)
	}
	if err := c.configureServer(cliOptions{listen: "", set: map[string]bool{"listen": true}}); err == nil {
		t.Fatal("empty HTTP address")
	}
	c.file.Mode = "invalid"
	if err := c.configureServer(cliOptions{}); err == nil {
		t.Fatal("invalid transport")
	}
	c.file.Mode = "server"
	for _, format := range []string{"base64", "file", "both"} {
		c.outputFormat = format
		store, err := newObjectStorage(c)
		if (err == nil) != (format == "base64") || store != nil {
			t.Fatalf("%s: %v", format, err)
		}
	}
	t.Setenv("S3_BUCKET", "bucket")
	t.Setenv("AWS_ACCESS_KEY_ID", "access")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "secret")
	t.Setenv("AWS_SESSION_TOKEN", "session")
	t.Setenv("AWS_ENDPOINT_URL_S3", "http://localhost:9000")
	t.Setenv("AWS_REGION", "auto")
	t.Setenv("S3_USE_PATH_STYLE", "true")
	t.Setenv("S3_PUBLIC_URL_PREFIX", "https://cdn.example/images")
	store, err := newObjectStorage(c)
	if err != nil || store.cfg.Region != "auto" || !store.cfg.UsePathStyle || store.ttl != 24*time.Hour || store.cfg.SessionToken != "session" {
		t.Fatal(store, err)
	}
	for _, test := range []struct {
		name string
		cfg  s3Config
	}{
		{"endpoint", s3Config{Endpoint: "ftp://invalid"}},
		{"public", s3Config{PublicURLPrefix: "https://user:password@example.com"}},
		{"ttl", s3Config{PresignTTL: "invalid"}},
		{"zero ttl", s3Config{PresignTTL: "0s"}},
		{"long ttl", s3Config{PresignTTL: "169h"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			c.file.S3 = test.cfg
			if _, err := newObjectStorage(c); err == nil {
				t.Fatal("invalid S3 accepted")
			}
		})
	}
	c.file.S3 = s3Config{}
	t.Setenv("S3_USE_PATH_STYLE", "invalid")
	if _, err := newObjectStorage(c); err == nil {
		t.Fatal("invalid boolean")
	}
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("mode: server\nserver:\n  listen: ':8088'\n  api_key: service-key\ns3:\n  bucket: configured\n  use_path_style: false\n  key_prefix: custom\n"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := loaded.configureServer(cliOptions{}); err != nil {
		t.Fatal(err)
	}
	parsed, err := newObjectStorage(loaded)
	if err != nil || parsed.cfg.UsePathStyle || parsed.cfg.Bucket != "configured" || loaded.file.Server.APIKey != "service-key" {
		t.Fatal(parsed, err)
	}
	c.file.Mode = "standalone"
	if store, err := newObjectStorage(c); store != nil || err != nil {
		t.Fatal("stdio changed", err)
	}
}

func TestHTTPS3Generation(t *testing.T) {
	for _, test := range []struct {
		name, format, public string
		failAt               int
		invalid, deleteFail  bool
	}{
		{name: "presigned", format: "file"},
		{name: "public", format: "file", public: "https://cdn.example/assets"},
		{name: "both", format: "both"},
		{name: "base64", format: "base64"},
		{name: "partial upload", format: "file", failAt: 2},
		{name: "invalid second image", format: "file", invalid: true},
		{name: "cleanup error", format: "file", failAt: 2, deleteFail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var mu sync.Mutex
			objects := map[string][]byte{}
			puts, deletes := 0, 0
			mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				switch r.Method {
				case "PUT":
					puts++
					if test.failAt == puts {
						w.WriteHeader(403)
						return
					}
					if r.Header.Get("Content-Type") != "image/png" || !strings.Contains(r.Header.Get("Authorization"), "Credential=access/") {
						t.Error("incorrect S3 headers")
					}
					data, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					objects[r.URL.Path] = data
					w.WriteHeader(200)
				case "GET":
					data, ok := objects[r.URL.Path]
					if !ok {
						w.WriteHeader(404)
						return
					}
					_, _ = w.Write(data)
				case "DELETE":
					deletes++
					if test.deleteFail {
						w.WriteHeader(403)
						return
					}
					delete(objects, r.URL.Path)
					w.WriteHeader(204)
				default:
					w.WriteHeader(405)
				}
			}))
			defer mock.Close()
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				second := "data:image/png;base64," + testPNG
				if test.invalid {
					second = "not-an-image"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]string{"b64_json": testPNG}, map[string]string{"b64_json": second}}})
			}))
			defer relay.Close()
			c := config{baseURL: relay.URL, apiKey: "relay-key", outputFormat: test.format, outputDir: filepath.Join(t.TempDir(), "unused"), file: fileConfig{Mode: "server", Server: serverConfig{APIKey: "service-key"}, S3: s3Config{Endpoint: mock.URL, Bucket: "bucket", Region: "us-east-1", AccessKeyID: "access", SecretAccessKey: "secret", UsePathStyle: true, PublicURLPrefix: test.public}}}
			store, err := newObjectStorage(c)
			if err != nil {
				t.Fatal(err)
			}
			host := httptest.NewServer(newHTTPHandler(&service{config: c, client: relay.Client(), storage: store}))
			defer host.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			data, _ := json.Marshal(restInput{Model: "gpt-image-2", Prompt: "draw"})
			req, _ := http.NewRequestWithContext(ctx, "POST", host.URL+"/v1/generate", bytes.NewReader(data))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer service-key")
			resp, err := host.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			responseBody, _ := io.ReadAll(resp.Body)
			failed := test.failAt > 0 || test.invalid
			if (resp.StatusCode != 200) != failed {
				t.Fatal(resp.StatusCode, string(responseBody))
			}
			mu.Lock()
			count, putCount, deleteCount := len(objects), puts, deletes
			mu.Unlock()
			if failed {
				if deleteCount != 1 || (!test.deleteFail && count != 0) {
					t.Fatalf("objects=%d deletes=%d", count, deleteCount)
				}
				return
			}
			var out generateOutput
			encoded := responseBody
			if err := json.Unmarshal(encoded, &out); err != nil || len(out.Images) != 2 {
				t.Fatal(string(encoded), err)
			}
			if _, err := os.Stat(c.outputDir); !os.IsNotExist(err) {
				t.Fatal("HTTP wrote local files", err)
			}
			for _, img := range out.Images {
				if img.Path != "" || img.Bytes <= 0 || img.MIMEType != "image/png" {
					t.Fatal(img)
				}
				if test.format == "base64" {
					if img.URL != "" || img.Base64 == "" || putCount != 0 {
						t.Fatal(img)
					}
					continue
				}
				if test.format == "both" && img.Base64 == "" {
					t.Fatal(img)
				}
				if test.public != "" {
					if !strings.HasPrefix(img.URL, test.public+"/images/") {
						t.Fatal(img.URL)
					}
					continue
				}
				u, err := url.Parse(img.URL)
				if err != nil {
					t.Fatal(err)
				}
				if u.Query().Get("X-Amz-Expires") != "86400" || !strings.HasPrefix(u.Path, "/bucket/images/"+time.Now().UTC().Format("2006/01/02")+"/") {
					t.Fatal(img.URL)
				}
				resp, err := http.Get(img.URL)
				if err != nil {
					t.Fatal(err)
				}
				data, _ := io.ReadAll(resp.Body)
				resp.Body.Close()
				raw, _ := decodeBase64(testPNG)
				if resp.StatusCode != 200 || !bytes.Equal(data, raw) {
					t.Fatal("S3 bytes differ")
				}
			}
		})
	}
}

// TestHTTPShutdown covers bind failure and cancellation without leaked watchers.
func TestHTTPShutdown(t *testing.T) {
	if err := serveHTTP(context.Background(), serverConfig{Listen: "invalid address"}, http.NotFoundHandler()); err == nil {
		t.Fatal("invalid listen accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := serveHTTP(ctx, serverConfig{Listen: "127.0.0.1:0"}, http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
}

// TestUploadEndpoint covers authenticated binary/multipart success and URL metadata,
// missing storage, invalid MIME/body/form, size limits, methods, and S3 failure.

func mustGetwd(t *testing.T) string {
	t.Helper()
	p, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return p
}
