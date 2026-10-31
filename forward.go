package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type forwardClient struct {
	cfg    forwardConfig
	client *http.Client
}

func newForwardClient(cfg forwardConfig) *forwardClient {
	timeout, _ := parseTimeout(cfg.Timeout)
	return &forwardClient{cfg: cfg, client: &http.Client{Timeout: timeout, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if len(via) > 0 && (r.URL.Scheme != via[0].URL.Scheme || r.URL.Host != via[0].URL.Host) {
			return fmt.Errorf("forward redirect changed origin")
		}
		return nil
	}}}
}
func (f *forwardClient) call(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, f.cfg.URL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	if f.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+f.cfg.APIKey)
	}
	resp, err := f.client.Do(req)
	if err != nil {
		return fmt.Errorf("forward request: %w", err)
	}
	defer resp.Body.Close()
	b, err := readLimited(resp.Body, 96<<20)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(b, &failure)
		return fmt.Errorf("remote HTTP %d: %s", resp.StatusCode, failure.Error)
	}
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("decode remote response: %w", err)
	}
	return nil
}
func (f *forwardClient) generate(ctx context.Context, in generateInput) (generateOutput, error) {
	var out generateOutput
	if len(in.InputImages) > 16 {
		return out, fmt.Errorf("at most 16 input images")
	}
	refs := append([]string(nil), in.InputImages...)
	files := map[string]*os.File{}
	var order []string
	defer func() {
		for _, file := range files {
			_ = file.Close()
		}
	}()
	total := int64(0)
	for i, ref := range refs {
		if strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") || strings.HasPrefix(ref, "data:") {
			continue
		}
		file, err := os.Open(ref)
		if err != nil {
			return out, fmt.Errorf("input_images[%d]: open local file: %w", i, err)
		}
		name := fmt.Sprintf("file_%d", i)
		files[name] = file
		order = append(order, name)
		stat, err := file.Stat()
		if err != nil {
			return out, err
		}
		if !stat.Mode().IsRegular() {
			return out, fmt.Errorf("input must be a regular file")
		}
		total += stat.Size()
		if stat.Size() > maxImageBytes || total > maxTotalInputBytes {
			return out, errInputSize
		}
		header := make([]byte, 512)
		n, err := file.Read(header)
		if err != nil && err != io.EOF {
			return out, err
		}
		mt := http.DetectContentType(header[:n])
		if mt != "image/png" && mt != "image/jpeg" && mt != "image/webp" {
			return out, fmt.Errorf("unsupported input image MIME")
		}
		if _, err = file.Seek(0, io.SeekStart); err != nil {
			return out, err
		}
		refs[i] = "part:" + name
	}
	in.InputImages = refs
	b, err := json.Marshal(publicInput(in))
	if err != nil {
		return out, err
	}
	if len(files) == 0 {
		err = f.call(ctx, "POST", "/v1/generate", "application/json", bytes.NewReader(b), &out)
		return out, err
	}
	reader, writer := io.Pipe()
	mw := multipart.NewWriter(writer)
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := mw.WriteField("request", string(b))
		for _, name := range order {
			if err != nil {
				break
			}
			var part io.Writer
			part, err = mw.CreateFormFile(name, name+".png")
			if err == nil {
				var n int64
				n, err = io.Copy(part, io.LimitReader(files[name], maxImageBytes+1))
				if n > maxImageBytes {
					err = errInputSize
				}
			}
		}
		if err == nil {
			err = mw.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	err = f.call(ctx, "POST", "/v1/generate", mw.FormDataContentType(), reader, &out)
	_ = reader.CloseWithError(fmt.Errorf("forward request completed"))
	<-done
	return out, err
}
func imageToolResult(out generateOutput) (*mcp.CallToolResult, error) {
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	result := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}
	for _, img := range out.Images {
		if img.Base64 != "" {
			data, err := base64.StdEncoding.DecodeString(img.Base64)
			if err != nil {
				return nil, fmt.Errorf("invalid remote image base64: %w", err)
			}
			result.Content = append(result.Content, &mcp.ImageContent{Data: data, MIMEType: img.MIMEType})
		}
	}
	return result, nil
}
func newForwardServer(cfg forwardConfig) *mcp.Server {
	f := newForwardClient(cfg)
	server := mcp.NewServer(&mcp.Implementation{Name: "images-mcp", Version: "0.4.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "generate_image", Description: "Generate or edit images. Local input_images paths are read and forwarded automatically. Call list_models for available models."}, func(ctx context.Context, _ *mcp.CallToolRequest, in restInput) (*mcp.CallToolResult, generateOutput, error) {
		input := generateInput{Prompt: in.Prompt, Model: in.Model, Size: in.Size, Quality: in.Quality, Timeout: in.Timeout, InputImages: in.InputImages}
		out, err := f.generate(ctx, input)
		if err != nil {
			return nil, out, err
		}
		result, err := imageToolResult(out)
		return result, out, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "list_models", Description: "List available remote image generation models."}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		var models []modelRecord
		if err := f.call(ctx, "GET", "/v1/models", "", nil, &models); err != nil {
			return nil, nil, err
		}
		b, _ := json.Marshal(models)
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(b)}}}, nil, nil
	})
	return server
}
