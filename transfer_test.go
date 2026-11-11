package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// transferFixture always carries Base64 to prove the server never transmits it.
func transferFixture(t *testing.T) generateOutput {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(testPNG)
	if err != nil {
		t.Fatal(err)
	}
	out := generateOutput{Model: "custom", Notice: "notice"}
	for _, data := range [][]byte{b, append(append([]byte{}, b...), 1)} {
		out.Images = append(out.Images, imageMetadata{MIMEType: "image/png", Bytes: len(data), data: data, Base64: base64.StdEncoding.EncodeToString(data)})
	}
	return out
}

func TestBinaryTransfer(t *testing.T) {
	for _, format := range []string{"file", "both", "base64"} {
		t.Run(format, func(t *testing.T) {
			expected := transferFixture(t)
			host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { writeGenerationResponse(w, expected) }))
			defer host.Close()
			dir := filepath.Join(t.TempDir(), "output")
			f := newForwardClient(forwardConfig{outputFormat: format, URL: host.URL, Timeout: "1m", outputDir: dir})
			out, err := f.generate(context.Background(), generateInput{Prompt: "draw"})
			if err != nil {
				t.Fatal(err)
			}
			if out.Model != expected.Model || out.Notice != expected.Notice || len(out.Images) != 2 {
				t.Fatalf("metadata: %+v", out)
			}
			for i, img := range out.Images {
				wantBase64 := ""
				if format != "file" {
					wantBase64 = base64.StdEncoding.EncodeToString(expected.Images[i].data)
				}
				if img.Bytes != expected.Images[i].Bytes || img.MIMEType != "image/png" || img.Base64 != wantBase64 {
					t.Fatal("image metadata differs")
				}
				if format == "base64" {
					if img.Path != "" {
						t.Fatal("base64 wrote path")
					}
					if _, err := os.Stat(dir); !os.IsNotExist(err) {
						t.Fatal("base64 created directory")
					}
					continue
				}
				if !filepath.IsAbs(img.Path) || filepath.Dir(img.Path) != dir {
					t.Fatal(img.Path)
				}
				b, err := os.ReadFile(img.Path)
				if err != nil || !bytes.Equal(b, expected.Images[i].data) {
					t.Fatal("incorrect file", err)
				}
				stat, _ := os.Stat(img.Path)
				if stat.Mode().Perm() != 0600 {
					t.Fatal(stat.Mode())
				}
			}
			if format != "base64" {
				stat, _ := os.Stat(dir)
				if stat.Mode().Perm() != 0700 {
					t.Fatal(stat.Mode())
				}
			}
			result, err := imageToolResult(out)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if format != "file" {
				want = 3
			}
			if len(result.Content) != want {
				t.Fatal("native image content missing")
			}
			for i, content := range result.Content[1:] {
				img, ok := content.(*mcp.ImageContent)
				if !ok || img.MIMEType != "image/png" || !bytes.Equal(img.Data, expected.Images[i].data) {
					t.Fatal("incorrect native image content")
				}
			}
			if format != "base64" {
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != len(out.Images) {
					t.Fatal("incorrect file count", err)
				}
			}
		})
	}
}

func TestForwardOutputDirectoryFallback(t *testing.T) {
	for _, env := range []bool{true, false} {
		t.Run(fmt.Sprint(env), func(t *testing.T) {
			dir := os.TempDir()
			t.Setenv("IMAGE_OUTPUT_DIR", "")
			if env {
				dir = t.TempDir()
				t.Setenv("IMAGE_OUTPUT_DIR", dir)
			}
			recorder := httptest.NewRecorder()
			writeGenerationResponse(recorder, transferFixture(t))
			c := config{file: fileConfig{Mode: "forward", Forward: forwardConfig{URL: "http://127.0.0.1:1"}}}
			if err := c.configureServer(cliOptions{}); err != nil {
				t.Fatal(err)
			}
			out, err := newForwardClient(c.file.Forward).decodeGeneration(context.Background(), recorder.Result())
			if err != nil {
				t.Fatal(err)
			}
			for _, img := range out.Images {
				defer os.Remove(img.Path)
				if filepath.Dir(img.Path) != dir {
					t.Fatal(img.Path)
				}
			}
		})
	}
}

func malformedTransfer(t *testing.T, kind string) *http.Response {
	t.Helper()
	fixture := transferFixture(t)
	manifest := transferManifest{Model: "custom", Images: []transferImage{{ContentID: "image-0", MIMEType: "image/png", Bytes: fixture.Images[0].Bytes}, {ContentID: "image-1", MIMEType: "image/png", Bytes: fixture.Images[1].Bytes}}}
	switch kind {
	case "total":
		manifest.Images = []transferImage{{ContentID: "image-0", Bytes: maxImageBytes}, {ContentID: "image-1", Bytes: maxImageBytes}, {ContentID: "image-2", Bytes: maxImageBytes}}
	case "count":
		manifest.Images = nil
	case "many":
		manifest.Images = make([]transferImage, 17)
	case "id":
		manifest.Images[1].ContentID = "image-0"
	case "length":
		manifest.Images[0].Bytes++
	case "large-image":
		manifest.Images[0].Bytes = maxImageBytes + 1
	case "mime":
		manifest.Images[0].MIMEType = "image/jpeg"
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{"Content-Type": {"application/json"}, "Content-ID": {"<manifest>"}}
	if kind == "manifest-header" {
		header.Set("Content-ID", "<wrong>")
	}
	part, _ := mw.CreatePart(header)
	if kind == "json" {
		_, _ = io.WriteString(part, "{")
	} else if kind == "format" || kind == "file-base64" {
		raw, _ := json.Marshal(manifest)
		field := `"output_format":"both",`
		if kind == "file-base64" {
			field = `"base64":"unexpected",`
		}
		_, _ = part.Write(append([]byte("{"+field), raw[1:]...))
	} else {
		_ = json.NewEncoder(part).Encode(manifest)
	}
	for i, img := range fixture.Images {
		if kind == "missing" && i == 1 {
			break
		}
		id := fmt.Sprintf("<image-%d>", i)
		if kind == "duplicate" && i == 1 {
			id = "<image-0>"
		}
		header = textproto.MIMEHeader{"Content-Type": {img.MIMEType}, "Content-ID": {id}}
		if kind == "encoding" {
			header.Set("Content-Transfer-Encoding", "base64")
		}
		if kind == "duplicate-header" {
			header.Add("Content-ID", id)
		}
		part, _ = mw.CreatePart(header)
		_, _ = part.Write(img.data)
	}
	if kind == "extra" {
		part, _ = mw.CreatePart(header)
		_, _ = part.Write(fixture.Images[0].data)
	}
	_ = mw.Close()
	raw := body.Bytes()
	if kind == "truncate" {
		raw = raw[:len(raw)-20]
	}
	resp := &http.Response{Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(raw)), ContentLength: int64(len(raw))}
	resp.Header.Set("Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mw.Boundary()}))
	if kind == "body-limit" {
		resp.ContentLength = maxResponseBytes + 1
	}
	if kind == "content-type" {
		resp.Header.Set("Content-Type", "text/html")
	}
	if kind == "boundary" {
		resp.Header.Set("Content-Type", "multipart/mixed")
	}
	return resp
}

func TestRejectMalformedTransfer(t *testing.T) {
	for _, kind := range []string{"total", "json", "count", "many", "id", "length", "large-image", "mime", "format", "file-base64", "manifest-header", "missing", "duplicate", "encoding", "duplicate-header", "extra", "truncate", "body-limit", "content-type", "boundary"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			f := newForwardClient(forwardConfig{Timeout: "1m", outputDir: dir})
			resp := malformedTransfer(t, kind)
			defer resp.Body.Close()
			if _, err := f.decodeGeneration(context.Background(), resp); err == nil {
				t.Fatal("accepted malformed response")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatal("partial files remain")
			}
		})
	}
}

func TestTransferCancellationAndWriteFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, kind := range []string{"cancel", "write"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			cfg := forwardConfig{Timeout: "1m", outputDir: dir}
			callCtx := ctx
			if kind == "write" {
				path := filepath.Join(dir, "regular-file")
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
				cfg.outputDir = path
				callCtx = context.Background()
			}
			rec := httptest.NewRecorder()
			writeGenerationResponse(rec, transferFixture(t))
			f := newForwardClient(cfg)
			if _, err := f.decodeGeneration(callCtx, rec.Result()); err == nil {
				t.Fatal("expected failure")
			}
			entries, _ := os.ReadDir(dir)
			want := 0
			if kind == "write" {
				want = 1
			}
			if len(entries) != want {
				t.Fatal("files remain")
			}
		})
	}
}

func TestRejectJSONSuccessResponse(t *testing.T) {
	valid, err := json.Marshal(transferFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"file", "base64", "both"} {
		dir := filepath.Join(t.TempDir(), "output")
		f := newForwardClient(forwardConfig{outputFormat: format, outputDir: dir})
		resp := &http.Response{Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(bytes.NewReader(valid))}
		if _, err := f.decodeGeneration(context.Background(), resp); err == nil {
			t.Fatal("accepted valid legacy JSON success response")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatal("JSON response created files")
		}
	}
}

func TestRESTBinaryGenerationEndToEnd(t *testing.T) {
	for _, format := range []string{"file", "both", "base64"} {
		t.Run(format, func(t *testing.T) {
			relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer relay-test" {
					t.Error("relay authorization")
				}
				respond(w, 200, map[string]any{"data": []any{map[string]string{"b64_json": testPNG}}})
			}))
			defer relay.Close()
			unused := filepath.Join(t.TempDir(), "server-output")
			cfg := config{baseURL: relay.URL, apiKey: "relay-test", outputDir: unused, outputFormat: format, file: fileConfig{Mode: "server", DefaultModel: "custom", Models: map[string]fileModel{"custom": {API: "images"}}, Server: serverConfig{APIKey: "service-test"}}}
			// Check the actual REST endpoint independently of forward normalization.
			svc := &service{config: cfg, client: relay.Client()}
			metadata, err := svc.saveImage(context.Background(), "data:image/png;base64,"+testPNG)
			if err != nil || metadata.Base64 != "" || len(metadata.data) == 0 {
				t.Fatal("server must retain only raw image bytes", err)
			}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest("POST", "/v1/generate", strings.NewReader(`{"prompt":"draw"}`))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer service-test")
			newHTTPHandler(svc).ServeHTTP(recorder, request)
			media, params, err := mime.ParseMediaType(recorder.Header().Get("Content-Type"))
			if recorder.Code != 200 || err != nil || media != "multipart/mixed" {
				t.Fatalf("REST format: %d %s", recorder.Code, media)
			}
			reader := multipart.NewReader(recorder.Body, params["boundary"])
			part, err := reader.NextRawPart()
			if err != nil {
				t.Fatal(err)
			}
			raw, err := io.ReadAll(part)
			if err != nil {
				t.Fatal(err)
			}
			var manifest map[string]any
			if err := json.Unmarshal(raw, &manifest); err != nil {
				t.Fatal(err)
			}
			if len(manifest) != 2 || manifest["model"] != "custom" {
				t.Fatalf("manifest: %s", raw)
			}
			for _, key := range []string{`"output_format"`, `"base64"`, `"url"`, `"path"`} {
				if bytes.Contains(raw, []byte(key)) {
					t.Fatalf("unexpected manifest field: %s", raw)
				}
			}
			images := manifest["images"].([]any)
			image := images[0].(map[string]any)
			data, _ := base64.StdEncoding.DecodeString(testPNG)
			if len(images) != 1 || len(image) != 3 || image["content_id"] != "image-0" || image["mime_type"] != "image/png" || image["bytes"] != float64(len(data)) {
				t.Fatalf("image metadata: %s", raw)
			}
			part, err = reader.NextRawPart()
			if err != nil {
				t.Fatal(err)
			}
			raw, err = io.ReadAll(part)
			if err != nil || !bytes.Equal(raw, data) || part.Header.Get("Content-Type") != "image/png" || part.Header.Get("Content-ID") != "<image-0>" {
				t.Fatal("invalid binary part", err)
			}
			if _, err = reader.NextRawPart(); err != io.EOF {
				t.Fatal("extra image part", err)
			}
			host := httptest.NewServer(newHTTPHandler(&service{config: cfg, client: relay.Client()}))
			defer host.Close()
			dir := t.TempDir()
			f := newForwardClient(forwardConfig{outputFormat: format, URL: host.URL, APIKey: "service-test", Timeout: "1m", outputDir: dir})
			out, err := f.generate(context.Background(), generateInput{Prompt: "draw"})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Images) != 1 || out.Model != "custom" {
				t.Fatalf("result: %+v", out)
			}
			img := out.Images[0]
			if format == "base64" {
				if img.Path != "" {
					t.Fatal("base64 path")
				}
			} else {
				b, err := os.ReadFile(img.Path)
				if err != nil || base64.StdEncoding.EncodeToString(b) != testPNG {
					t.Fatal("file differs", err)
				}
			}
			if (img.Base64 != "") != (format != "file") {
				t.Fatal("wrong base64 semantics")
			}
			if _, err := os.Stat(unused); !os.IsNotExist(err) {
				t.Fatal("server wrote files")
			}
		})
	}
}

func TestForwardInterruptedResponse(t *testing.T) {
	started := make(chan struct{})
	finished := make(chan struct{})
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		w.Header().Set("Content-Type", "multipart/mixed; boundary=example")
		_, _ = io.WriteString(w, "--example\r\nContent-Type: application/json\r\nContent-ID: <manifest>\r\n\r\n{")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer host.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	f := newForwardClient(forwardConfig{URL: host.URL, Timeout: "1m", outputDir: dir})
	result := make(chan error, 1)
	go func() { _, err := f.generate(ctx, generateInput{Prompt: "draw"}); result <- err }()
	<-started
	cancel()
	if err := <-result; err == nil {
		t.Fatal("cancellation accepted")
	}
	<-finished
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("partial files remain")
	}
}

func TestForwardErrorDoesNotEchoSecrets(t *testing.T) {
	host := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failHTTP(w, 500, fmt.Errorf("https://example.invalid/image?signature=secret relay-key service-key"))
	}))
	defer host.Close()
	f := newForwardClient(forwardConfig{URL: host.URL, APIKey: "service-key", Timeout: "1m"})
	_, err := f.generate(context.Background(), generateInput{Prompt: "draw"})
	if err == nil || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "key") || strings.Contains(err.Error(), "http:") {
		t.Fatal("unsafe error", err)
	}
}

// Cancel deterministically once a file exists, to exercise rollback after a
// successful write without depending on disk speed or scheduler timing.
type cancelOnFileContext struct {
	context.Context
	dir    string
	cancel context.CancelFunc
}

func (c cancelOnFileContext) Err() error {
	entries, _ := os.ReadDir(c.dir)
	if len(entries) > 0 {
		c.cancel()
	}
	return c.Context.Err()
}
func TestForwardRollsBackCreatedFiles(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	rec := httptest.NewRecorder()
	writeGenerationResponse(rec, transferFixture(t))
	f := newForwardClient(forwardConfig{Timeout: "1m", outputDir: dir})
	out, err := f.decodeGeneration(cancelOnFileContext{Context: ctx, dir: dir, cancel: cancel}, rec.Result())
	if err != context.Canceled || len(out.Images) != 0 {
		t.Fatal("expected canceled empty result", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("created files were not removed")
	}
}

type zeroResponseReader struct{}

func (zeroResponseReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }
func TestForwardBoundsChunkedResponse(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGenerationResponse(rec, transferFixture(t))
	resp := rec.Result()
	defer resp.Body.Close()
	// An unbounded epilogue must still count against the response body limit.
	resp.Body = io.NopCloser(io.MultiReader(resp.Body, zeroResponseReader{}))
	resp.ContentLength = -1
	dir := t.TempDir()
	f := newForwardClient(forwardConfig{Timeout: "1m", outputDir: dir})
	if _, err := f.decodeGeneration(context.Background(), resp); err == nil {
		t.Fatal("unbounded response accepted")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatal("oversize response wrote files")
	}
}

func TestForwardLocalOutputConfig(t *testing.T) {
	tests := []struct {
		top, def, env, want string
		invalid             bool
	}{
		{"", "", "", "file", false},
		{"", "", "BASE64", "base64", false},
		{"", "BoTh", "base64", "both", false},
		{"FILE", "both", "base64", "file", false},
		{"invalid", "", "", "", true},
		{"file", "invalid", "", "", true},
		{"file", "", "invalid", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.top+"/"+tt.def+"/"+tt.env, func(t *testing.T) {
			t.Setenv("IMAGE_OUTPUT_FORMAT", tt.env)
			envDir := t.TempDir()
			t.Setenv("IMAGE_OUTPUT_DIR", envDir)
			localDir := filepath.Join(t.TempDir(), "output")
			c := config{file: fileConfig{Mode: "forward", OutputFormat: tt.top, OutputDir: localDir,
				Default: fileCredentials{OutputFormat: tt.def, Timeout: "invalid"},
				Models:  map[string]fileModel{"unused": {API: "invalid"}},
				Forward: forwardConfig{URL: "http://127.0.0.1:1", Timeout: "1m"},
			}}
			err := c.configureServer(cliOptions{})
			if tt.invalid {
				if err == nil {
					t.Fatal("accepted invalid output format")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			f := newForwardClient(c.file.Forward)
			t.Setenv("IMAGE_OUTPUT_FORMAT", "file")
			if f.outputFormat != tt.want || f.cfg.outputDir != localDir {
				t.Fatalf("wrong local output settings: %s %s", f.outputFormat, f.cfg.outputDir)
			}
			if _, err := os.Stat(localDir); !os.IsNotExist(err) {
				t.Fatal("startup created output directory")
			}
		})
	}
}

func TestWriteGenerationResponseRejectsLimits(t *testing.T) {
	fixture := transferFixture(t)
	many := generateOutput{Model: "custom"}
	for range 17 {
		many.Images = append(many.Images, fixture.Images[0])
	}
	empty := generateOutput{Model: "custom", Images: []imageMetadata{{MIMEType: "image/png"}}}
	for name, out := range map[string]generateOutput{"count": many, "empty": empty} {
		t.Run(name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			writeGenerationResponse(rec, out)
			if rec.Code != 500 || rec.Header().Get("Content-Type") != "application/json" {
				t.Fatalf("limit accepted: %d %s", rec.Code, rec.Header().Get("Content-Type"))
			}
		})
	}
}
