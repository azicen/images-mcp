package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"
	"strconv"
	"strings"
)

type imageMetadata struct {
	Path     string `json:"path,omitempty"`
	Base64   string `json:"base64,omitempty"`
	data     []byte
	MIMEType string `json:"mime_type"`
	Bytes    int    `json:"bytes"`
}
type generateOutput struct {
	Model  string          `json:"model"`
	Notice string          `json:"notice,omitempty"`
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

func trimmedStrings(values []string) []string {
	var qualities []string
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			qualities = append(qualities, value)
		}
	}
	return qualities
}

func (c config) resolveQuality(in generateInput) (generateInput, string, error) {
	family, err := c.modelFamily(in)
	if err != nil {
		return in, "", err
	}
	if family == "gemini" && in.Quality != "" {
		notice := fmt.Sprintf("model %q does not support quality setting; ignored quality %q", in.Model, in.Quality)
		in.Quality = ""
		return in, notice, nil
	}
	if family != "gpt-image" {
		return in, "", nil
	}
	model := c.file.Models[in.Model]
	qualities := trimmedStrings(model.Qualities)
	defaultQuality := strings.TrimSpace(model.DefaultQuality)
	quality := strings.TrimSpace(in.Quality)
	if quality == "" {
		in.Quality = defaultQuality
		return in, "", nil
	}
	in.Quality = quality
	if len(qualities) == 0 {
		return in, "", nil
	}
	for _, supported := range qualities {
		if strings.EqualFold(quality, supported) {
			in.Quality = supported
			return in, "", nil
		}
	}
	target := defaultQuality
	if target == "" {
		target = qualities[0]
	}
	notice := fmt.Sprintf("quality %q is not supported for model %q, degraded to default quality %q (supported: %s)", in.Quality, in.Model, target, strings.Join(qualities, ", "))
	in.Quality = target
	return in, notice, nil
}

// resolveSize normalizes configured presets and bounds custom sizes before relay I/O.
func (c config) resolveSize(in generateInput) (generateInput, string, error) {
	family, err := c.modelFamily(in)
	if err != nil {
		return in, "", err
	}
	if family == "gemini" && in.Size != "" {
		notice := fmt.Sprintf("model %q does not support size setting; ignored size %q", in.Model, in.Size)
		in.Size = ""
		return in, notice, nil
	}
	if family != "gpt-image" {
		return in, "", nil
	}
	model := c.file.Models[in.Model]
	sizes := trimmedStrings(model.Sizes)
	defaultSize := strings.TrimSpace(model.DefaultSize)
	rawSize := strings.TrimSpace(in.Size)
	if rawSize == "" {
		in.Size = defaultSize
		return in, "", nil
	}
	for _, size := range sizes {
		if strings.EqualFold(rawSize, size) {
			in.Size = size
			return in, "", nil
		}
	}
	if !model.SupportsCustomSize {
		return in, "", fmt.Errorf("model %q does not support custom size %q; supported presets: %s", in.Model, rawSize, strings.Join(sizes, ", "))
	}
	snapped, err := snapSize(rawSize)
	if err != nil {
		if errors.Is(err, strconv.ErrSyntax) {
			return in, "", fmt.Errorf("invalid size %q for model %q: expected WIDTHxHEIGHT (e.g. 1536x864) or presets (%s)", rawSize, in.Model, strings.Join(sizes, ", "))
		}
		return in, "", fmt.Errorf("invalid size %q for model %q: %w", rawSize, in.Model, err)
	}
	in.Size = snapped
	if snapped != rawSize {
		return in, fmt.Sprintf("size %q adjusted to %q (aligned to nearest multiple of 16)", rawSize, snapped), nil
	}
	return in, "", nil
}

func snapSize(raw string) (string, error) {
	width, height, ok := strings.Cut(strings.ToLower(raw), "x")
	if !ok {
		return "", strconv.ErrSyntax
	}
	w, errW := strconv.ParseUint(width, 10, 64)
	h, errH := strconv.ParseUint(height, 10, 64)
	if errors.Is(errW, strconv.ErrSyntax) || errors.Is(errH, strconv.ErrSyntax) || w == 0 || h == 0 {
		return "", strconv.ErrSyntax
	}
	// Check before rounding to avoid overflow; 3847 still rounds down to 3840.
	if errW != nil || errH != nil || w > 3847 || h > 3847 {
		return "", fmt.Errorf("each side must be at most 3840px after alignment")
	}
	w = max(16, ((w+8)/16)*16)
	h = max(16, ((h+8)/16)*16)
	if w > 3*h || h > 3*w {
		return "", fmt.Errorf("aspect ratio must be between 1:3 and 3:1 after alignment")
	}
	total := w * h
	if total < 655360 || total > 8294400 {
		return "", fmt.Errorf("total pixels must be between 655,360 and 8,294,400 after alignment")
	}
	return fmt.Sprintf("%dx%d", w, h), nil
}

func route(in generateInput) (string, error) {
	return (config{}).route(in)
}

func (c config) route(in generateInput) (string, error) {
	if strings.TrimSpace(in.Prompt) == "" {
		return "", fmt.Errorf("prompt is required")
	}
	sources, err := in.normalizedInputImages()
	if err != nil {
		return "", err
	}
	family, err := c.modelFamily(in)
	if err != nil {
		return "", err
	}
	switch family {
	case "gemini":
		return "/chat/completions", nil
	case "gpt-image":
		if len(sources) > 0 {
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
	in, _, err = s.config.resolveQuality(in)
	if err != nil {
		return nil, "", err
	}
	in, _, err = s.config.resolveSize(in)
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
	images, err := s.loadInputImages(ctx, in)
	if err != nil {
		return nil, "", err
	}
	var body bytes.Buffer
	contentType := "application/json"
	if endpoint == "/images/edits" {
		writer := multipart.NewWriter(&body)
		for _, field := range []struct{ name, value string }{{"model", in.Model}, {"prompt", in.Prompt}, {"size", in.Size}, {"quality", in.Quality}} {
			if field.value != "" {
				if err := writer.WriteField(field.name, field.value); err != nil {
					return nil, "", err
				}
			}
		}
		fieldName := "image"
		if len(images) > 1 {
			fieldName = "image[]"
		}
		for i, img := range images {
			header := make(textproto.MIMEHeader)
			header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="input%d%s"`, fieldName, i, imageExtension(img.mimeType)))
			header.Set("Content-Type", img.mimeType)
			part, err := writer.CreatePart(header)
			if err != nil {
				return nil, "", err
			}
			if _, err := part.Write(img.data); err != nil {
				return nil, "", err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, "", err
		}
		contentType = writer.FormDataContentType()
	} else {
		payload := map[string]any{"model": in.Model}
		if endpoint == "/chat/completions" {
			var content any = in.Prompt
			if len(images) > 0 {
				parts := []any{map[string]any{"type": "text", "text": in.Prompt}}
				for _, img := range images {
					parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + img.mimeType + ";base64," + base64.StdEncoding.EncodeToString(img.data)}})
				}
				content = parts
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
	// Resolve notices from original input; request resolves each parameter once
	// so configured defaults do not undergo a second resolution.
	_, notice, err := s.config.resolveQuality(in)
	if err != nil {
		return out, err
	}
	_, sizeNotice, err := s.config.resolveSize(in)
	if err != nil {
		return out, err
	}
	if sizeNotice != "" {
		if notice != "" {
			notice += "; "
		}
		notice += sizeNotice
	}
	timeout, err := s.config.resolveTimeout(in)
	if err != nil {
		return out, err
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
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
	out = generateOutput{Model: in.Model, Notice: notice, Images: []imageMetadata{}}
	defer func() {
		if err != nil {
			for _, img := range out.Images {
				if img.Path != "" {
					_ = os.Remove(img.Path)
				}
			}
		}
	}()
	totalOutput := 0
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
		totalOutput += img.Bytes
		if s.config.file.Mode == "server" && totalOutput > maxTotalInputBytes {
			return out, fmt.Errorf("generated images exceed total limit")
		}
	}
	return out, nil
}
