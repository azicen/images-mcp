package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	maxImageBytes      = 32 << 20
	maxTotalInputBytes = 64 << 20
)

type inputImageSource struct{ ref, source string }
type inputImage struct {
	data     []byte
	mimeType string
}

func (s *service) loadInputImages(ctx context.Context, in generateInput) ([]inputImage, error) {
	sources, err := in.normalizedInputImages()
	if err != nil {
		return nil, err
	}
	var images []inputImage
	total := 0
	for _, source := range sources {
		b, mimeType, err := s.loadImage(ctx, source.ref, true)
		if err != nil {
			return nil, fmt.Errorf("load %s: %w", source.source, err)
		}
		total += len(b)
		if total > maxTotalInputBytes {
			return nil, fmt.Errorf("load %s: total input images exceed %d bytes (64 MiB)", source.source, maxTotalInputBytes)
		}
		images = append(images, inputImage{data: b, mimeType: mimeType})
	}
	return images, nil
}

var (
	markdownImage = regexp.MustCompile(`!\[[^\]]*\]\(\s*(<[^>]+>|[^\s)]+)(?:\s+"[^"]*")?\s*\)`)
	dataImage     = regexp.MustCompile(`data:image/[a-zA-Z0-9.+-]+;base64,[A-Za-z0-9+/=_-]+`)
	imageURL      = regexp.MustCompile(`https?://[^\s<>"` + "`" + `]+`)
)

func parseText(text string) []string {
	var refs []string
	for _, match := range markdownImage.FindAllStringSubmatch(text, -1) {
		refs = append(refs, strings.Trim(match[1], "<>"))
	}
	text = markdownImage.ReplaceAllString(text, " ")
	refs = append(refs, dataImage.FindAllString(text, -1)...)
	text = dataImage.ReplaceAllString(text, " ")
	for _, match := range imageURL.FindAllString(text, -1) {
		refs = append(refs, strings.TrimRight(match, ").,;"))
	}
	text = strings.TrimSpace(imageURL.ReplaceAllString(text, " "))
	text = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(text, "```"), "```base64"))
	if decoded, err := decodeBase64(text); err == nil && imageExtension(http.DetectContentType(decoded)) != "" {
		refs = append(refs, text)
	}
	return refs
}

func parseResponse(body []byte, endpoint string) ([]string, error) {
	var response struct {
		Error json.RawMessage `json:"error"`
		Data  []struct {
			URL    string `json:"url"`
			Base64 string `json:"b64_json"`
		} `json:"data"`
		Choices []struct {
			Message struct {
				Content json.RawMessage `json:"content"`
				Images  []struct {
					ImageURL struct {
						URL string `json:"url"`
					} `json:"image_url"`
				} `json:"images"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("decode relay response: %w", err)
	}
	if len(response.Error) > 0 && string(response.Error) != "null" {
		return nil, fmt.Errorf("relay returned an API error")
	}
	var refs []string
	if endpoint == "/chat/completions" {
		for _, choice := range response.Choices {
			var text string
			if json.Unmarshal(choice.Message.Content, &text) == nil {
				refs = append(refs, parseText(text)...)
			} else {
				var parts []struct {
					Text     string          `json:"text"`
					ImageURL json.RawMessage `json:"image_url"`
				}
				if len(choice.Message.Content) > 0 && string(choice.Message.Content) != "null" {
					if err := json.Unmarshal(choice.Message.Content, &parts); err != nil {
						return nil, fmt.Errorf("decode chat content: %w", err)
					}
				}
				for _, part := range parts {
					refs = append(refs, parseText(part.Text)...)
					var imageRef string
					if json.Unmarshal(part.ImageURL, &imageRef) != nil {
						var obj struct {
							URL string `json:"url"`
						}
						_ = json.Unmarshal(part.ImageURL, &obj)
						imageRef = obj.URL
					}
					if imageRef != "" {
						refs = append(refs, imageRef)
					}
				}
			}
			for _, img := range choice.Message.Images {
				if img.ImageURL.URL != "" {
					refs = append(refs, img.ImageURL.URL)
				}
			}
		}
	} else {
		for _, img := range response.Data {
			if img.Base64 != "" {
				refs = append(refs, img.Base64)
			} else if img.URL != "" {
				refs = append(refs, img.URL)
			}
		}
	}
	unique := make([]string, 0, len(refs))
	seen := make(map[string]bool)
	for _, ref := range refs {
		if !seen[ref] {
			unique = append(unique, ref)
			seen[ref] = true
		}
	}
	if len(unique) == 0 {
		return nil, fmt.Errorf("relay response contains no images")
	}
	if len(unique) > 16 {
		return nil, fmt.Errorf("relay response exceeds 16 images")
	}
	return unique, nil
}

func decodeBase64(ref string) ([]byte, error) {
	ref = strings.Join(strings.Fields(ref), "")
	if len(ref) > base64.StdEncoding.EncodedLen(maxImageBytes) {
		return nil, fmt.Errorf("base64 image exceeds size limit")
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := encoding.DecodeString(ref); err == nil {
			if len(b) > maxImageBytes {
				return nil, fmt.Errorf("image exceeds size limit")
			}
			return b, nil
		}
	}
	return nil, fmt.Errorf("invalid base64 image")
}

func imageExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

func (s *service) loadImage(ctx context.Context, ref string, allowFile bool) ([]byte, string, error) {
	var b []byte
	var err error
	switch {
	case strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://"):
		u, parseErr := url.Parse(ref)
		if parseErr != nil || u.Host == "" || u.User != nil {
			return nil, "", fmt.Errorf("invalid image URL")
		}
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
		if reqErr != nil {
			return nil, "", reqErr
		}
		resp, getErr := s.client.Do(req)
		if getErr != nil {
			return nil, "", fmt.Errorf("download image: %w", getErr)
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, "", fmt.Errorf("image download returned HTTP %d", resp.StatusCode)
		}
		b, err = readLimited(resp.Body, maxImageBytes)
	case strings.HasPrefix(ref, "data:"):
		header, encoded, ok := strings.Cut(ref, ",")
		if !ok || !strings.HasPrefix(header, "data:image/") || !strings.HasSuffix(header, ";base64") {
			return nil, "", fmt.Errorf("invalid image data URI")
		}
		b, err = decodeBase64(encoded)
	default:
		b, err = decodeBase64(ref)
		if err != nil && allowFile {
			f, openErr := os.Open(ref)
			if openErr != nil {
				return nil, "", fmt.Errorf("input is neither a readable file nor valid base64")
			}
			defer f.Close()
			b, err = readLimited(f, maxImageBytes)
		}
	}
	if err != nil {
		return nil, "", err
	}
	mimeType := http.DetectContentType(b)
	if allowFile && mimeType != "image/png" && mimeType != "image/jpeg" && mimeType != "image/webp" {
		return nil, "", fmt.Errorf("unsupported input image MIME %q (expected PNG, JPEG, or WebP)", mimeType)
	}
	if imageExtension(mimeType) == "" {
		return nil, "", fmt.Errorf("unsupported image data (expected PNG, JPEG, GIF, or WebP)")
	}
	return b, mimeType, nil
}

func (s *service) saveImage(ctx context.Context, ref string) (imageMetadata, error) {
	b, mimeType, err := s.loadImage(ctx, ref, false)
	if err != nil {
		return imageMetadata{}, err
	}
	if err := ctx.Err(); err != nil {
		return imageMetadata{}, err
	}
	metadata := imageMetadata{MIMEType: mimeType, Bytes: len(b)}
	if s.config.outputFormat == "base64" || s.config.outputFormat == "both" {
		metadata.Base64 = base64.StdEncoding.EncodeToString(b)
		metadata.data = b
	}
	if s.config.outputFormat == "base64" {
		return metadata, nil
	}
	if err := os.MkdirAll(s.config.outputDir, 0700); err != nil {
		return imageMetadata{}, fmt.Errorf("create output directory: %w", err)
	}
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return imageMetadata{}, fmt.Errorf("generate filename: %w", err)
	}
	path, err := filepath.Abs(filepath.Join(s.config.outputDir, time.Now().UTC().Format("20060102T150405.000000000Z")+"_"+hex.EncodeToString(suffix)+imageExtension(mimeType)))
	if err != nil {
		return imageMetadata{}, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return imageMetadata{}, fmt.Errorf("create image: %w", err)
	}
	_, writeErr := f.Write(b)
	closeErr := f.Close()
	if writeErr != nil || closeErr != nil {
		_ = os.Remove(path)
		if writeErr != nil {
			return imageMetadata{}, fmt.Errorf("write image: %w", writeErr)
		}
		return imageMetadata{}, fmt.Errorf("close image: %w", closeErr)
	}
	metadata.Path = path
	return metadata, nil
}
