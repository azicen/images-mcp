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
	"net/textproto"
	"os"
	"strings"
)

type generateInput struct {
	Prompt     string `json:"prompt" jsonschema:"Generation or editing instructions"`
	Model      string `json:"model" jsonschema:"Gemini or GPT Image model name"`
	InputImage string `json:"input_image,omitempty" jsonschema:"Optional local path, HTTP(S) URL, data URI, or raw base64 image"`
	Size       string `json:"size,omitempty" jsonschema:"Optional GPT Image size"`
	Quality    string `json:"quality,omitempty" jsonschema:"Optional GPT Image quality"`
}
type imageMetadata struct {
	Path     string `json:"path"`
	MIMEType string `json:"mime_type"`
	Bytes    int    `json:"bytes"`
}
type generateOutput struct {
	Model  string          `json:"model"`
	Route  string          `json:"route"`
	Images []imageMetadata `json:"images"`
}
type service struct {
	config config
	client *http.Client
}

func route(in generateInput) (string, error) {
	if strings.TrimSpace(in.Prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}
	switch {
	case strings.HasPrefix(strings.ToLower(in.Model), "gemini"):
		if in.Size != "" || in.Quality != "" {
			return "", fmt.Errorf("size and quality are only supported for GPT Image")
		}
		return "/chat/completions", nil
	case strings.HasPrefix(strings.ToLower(in.Model), "gpt-image"):
		if in.InputImage != "" {
			return "/images/edits", nil
		}
		return "/images/generations", nil
	default:
		return "", fmt.Errorf("unsupported model family: use gemini or gpt-image")
	}
}

func (s *service) request(ctx context.Context, in generateInput) (*http.Request, string, error) {
	endpoint, err := route(in)
	if err != nil {
		return nil, "", err
	}
	var body bytes.Buffer
	contentType := "application/json"
	if endpoint == "/images/edits" {
		img, mimeType, err := s.loadImage(ctx, in.InputImage, true)
		if err != nil {
			return nil, "", fmt.Errorf("load input image: %w", err)
		}
		writer := multipart.NewWriter(&body)
		for _, field := range []struct{ name, value string }{{"model", in.Model}, {"prompt", in.Prompt}, {"size", in.Size}, {"quality", in.Quality}} {
			if field.value != "" {
				if err := writer.WriteField(field.name, field.value); err != nil {
					return nil, "", err
				}
			}
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image"; filename="input%s"`, imageExtension(mimeType)))
		header.Set("Content-Type", mimeType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(img); err != nil {
			return nil, "", err
		}
		if err := writer.Close(); err != nil {
			return nil, "", err
		}
		contentType = writer.FormDataContentType()
	} else {
		payload := map[string]any{"model": in.Model}
		if endpoint == "/chat/completions" {
			var content any = in.Prompt
			if in.InputImage != "" {
				img, mimeType, err := s.loadImage(ctx, in.InputImage, true)
				if err != nil {
					return nil, "", fmt.Errorf("load input image: %w", err)
				}
				content = []any{map[string]any{"type": "text", "text": in.Prompt}, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(img)}}}
			}
			payload["messages"] = []any{map[string]any{"role": "user", "content": content}}
			payload["stream"] = false
		} else {
			payload["prompt"] = in.Prompt
			if in.Size != "" {
				payload["size"] = in.Size
			}
			if in.Quality != "" {
				payload["quality"] = in.Quality
			}
		}
		if err := json.NewEncoder(&body).Encode(payload); err != nil {
			return nil, "", fmt.Errorf("encode request: %w", err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.config.baseURL+endpoint, &body)
	if err != nil {
		return nil, "", fmt.Errorf("create relay request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+s.config.apiKey)
	return req, endpoint, nil
}

func readLimited(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return b, nil
}

func (s *service) generate(ctx context.Context, in generateInput) (out generateOutput, err error) {
	req, endpoint, err := s.request(ctx, in)
	if err != nil {
		return out, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return out, fmt.Errorf("relay request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return out, fmt.Errorf("relay returned HTTP %d", resp.StatusCode)
	}
	body, err := readLimited(resp.Body, 64<<20)
	if err != nil {
		return out, fmt.Errorf("read relay response: %w", err)
	}
	refs, err := parseResponse(body, endpoint)
	if err != nil {
		return out, err
	}
	out = generateOutput{Model: in.Model, Route: endpoint, Images: []imageMetadata{}}
	defer func() {
		if err != nil {
			for _, img := range out.Images {
				_ = os.Remove(img.Path)
			}
		}
	}()
	for _, ref := range refs {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		var img imageMetadata
		img, err = s.saveImage(ctx, ref)
		if err != nil {
			return out, fmt.Errorf("save generated image: %w", err)
		}
		out.Images = append(out.Images, img)
	}
	return out, nil
}
