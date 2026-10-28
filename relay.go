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
	API        string `json:"api,omitempty" jsonschema:"Optional API route: gemini or chat; gpt, gpt-image or images; overrides model configuration"`
	APIType    string `json:"api_type,omitempty" jsonschema:"Alias for api"`
	BaseURL    string `json:"base_url,omitempty" jsonschema:"Optional relay base URL; overrides model family and OPENAI_BASE_URL defaults"`
	APIKey     string `json:"api_key,omitempty" jsonschema:"Optional relay API key; overrides model family and OPENAI_API_KEY defaults"`
	Prompt     string `json:"prompt" jsonschema:"Generation or editing instructions"`
	Model      string `json:"model,omitempty" jsonschema:"Optional relay model name; defaults to configured default_model. See tool description and list_models for available models."`
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

// explicitFamily normalizes both aliases and rejects ambiguous selections.
func explicitFamily(api, apiType string) (string, error) {
	var family string
	for _, field := range []struct{ name, value string }{{"api", api}, {"api_type", apiType}} {
		if field.value == "" {
			continue
		}
		var selected string
		switch strings.ToLower(field.value) {
		case "gemini", "chat":
			selected = "gemini"
		case "gpt", "gpt-image", "images":
			selected = "gpt-image"
		default:
			return "", fmt.Errorf("invalid %s: expected gemini, chat, gpt, gpt-image, or images", field.name)
		}
		if family != "" && family != selected {
			return "", fmt.Errorf("api and api_type select conflicting routes")
		}
		family = selected
	}
	return family, nil
}

func (c config) modelFamily(in generateInput) (string, error) {
	family, err := explicitFamily(in.API, in.APIType)
	if err != nil || family != "" {
		return family, err
	}
	model := c.file.Models[in.Model]
	family, err = explicitFamily(model.API, model.APIType)
	if err != nil || family != "" {
		return family, err
	}
	switch {
	case strings.HasPrefix(strings.ToLower(in.Model), "gemini"):
		return "gemini", nil
	case strings.HasPrefix(strings.ToLower(in.Model), "gpt-image"):
		return "gpt-image", nil
	default:
		return "", nil
	}
}

func route(in generateInput) (string, error) {
	return (config{}).route(in)
}

func (c config) route(in generateInput) (string, error) {
	if strings.TrimSpace(in.Prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}
	family, err := c.modelFamily(in)
	if err != nil {
		return "", err
	}
	switch family {
	case "gemini":
		if in.Size != "" || in.Quality != "" {
			return "", fmt.Errorf("size and quality are only supported for GPT Image")
		}
		return "/chat/completions", nil
	case "gpt-image":
		if in.InputImage != "" {
			return "/images/edits", nil
		}
		return "/images/generations", nil
	default:
		return "", fmt.Errorf("unsupported model %q: use gemini or gpt-image prefixes, or specify api/api_type; available configured models: %s; call list_models for details", in.Model, strings.Join(c.modelIDs(), ", "))
	}
}

func (c config) defaultInput(in generateInput) (generateInput, error) {
	if in.Model == "" {
		in.Model = c.file.DefaultModel
	}
	if in.Model == "" {
		return in, fmt.Errorf("model is required: supply model or configure default_model; available configured models: %s; call list_models for details", strings.Join(c.modelIDs(), ", "))
	}
	return in, nil
}

func (s *service) request(ctx context.Context, in generateInput) (*http.Request, string, error) {
	in, err := s.config.defaultInput(in)
	if err != nil {
		return nil, "", err
	}
	endpoint, err := s.config.route(in)
	if err != nil {
		return nil, "", err
	}
	resolved, err := s.config.resolveCredentials(in)
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
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, resolved.baseURL+endpoint, &body)
	if err != nil {
		return nil, "", fmt.Errorf("create relay request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+resolved.apiKey)
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
	in, err = s.config.defaultInput(in)
	if err != nil {
		return out, err
	}
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
