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
	"net/textproto"
	"net/url"
	"os"
	"strings"
)

const maxResponseBytes = 160 << 20
const maxManifestBytes = 1 << 20

type transferImage struct {
	ContentID string `json:"content_id"`
	MIMEType  string `json:"mime_type"`
	Bytes     int    `json:"bytes"`
}
type transferManifest struct {
	Model  string          `json:"model"`
	Notice string          `json:"notice,omitempty"`
	Images []transferImage `json:"images"`
}

func validateForwardURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(value, "#") {
		return fmt.Errorf("expected HTTP(S) URL without credentials, query, or fragment")
	}
	return nil
}

func writeGenerationResponse(w http.ResponseWriter, out generateOutput) {
	manifest := transferManifest{Model: out.Model, Notice: out.Notice}
	total := 0
	for i, img := range out.Images {
		total += len(img.data)
		if len(img.data) == 0 || len(img.data) > maxImageBytes || total > maxTotalInputBytes || i >= 16 {
			failHTTP(w, 500, fmt.Errorf("generated images exceed response limits"))
			return
		}
		manifest.Images = append(manifest.Images, transferImage{ContentID: fmt.Sprintf("image-%d", i), MIMEType: img.MIMEType, Bytes: img.Bytes})
	}
	mw := multipart.NewWriter(w)
	w.Header().Set("Content-Type", mime.FormatMediaType("multipart/mixed", map[string]string{"boundary": mw.Boundary()}))
	header := textproto.MIMEHeader{"Content-Type": {"application/json"}, "Content-ID": {"<manifest>"}}
	part, err := mw.CreatePart(header)
	if err == nil {
		err = json.NewEncoder(part).Encode(manifest)
	}
	for i, img := range out.Images {
		if err != nil {
			break
		}
		header = textproto.MIMEHeader{"Content-Type": {img.MIMEType}, "Content-ID": {fmt.Sprintf("<image-%d>", i)}}
		part, err = mw.CreatePart(header)
		if err == nil {
			_, err = part.Write(img.data)
		}
	}
	if err == nil {
		_ = mw.Close()
	}
}

func strictJSON(data []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return fmt.Errorf("invalid remote JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return fmt.Errorf("invalid remote JSON trailer")
	}
	return nil
}

// decodeGeneration validates the entire bounded response before creating any files.
func (f *forwardClient) decodeGeneration(ctx context.Context, resp *http.Response) (out generateOutput, err error) {
	media, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil {
		return out, fmt.Errorf("invalid response Content-Type")
	}
	if media != "multipart/mixed" {
		return out, fmt.Errorf("unexpected generation Content-Type")
	}
	if params["boundary"] == "" {
		return out, fmt.Errorf("missing response boundary")
	}
	if resp.ContentLength > maxResponseBytes {
		return out, fmt.Errorf("response too large")
	}
	body := &io.LimitedReader{R: resp.Body, N: maxResponseBytes + 1}
	mr := multipart.NewReader(body, params["boundary"])
	part, err := mr.NextRawPart()
	if err != nil {
		return out, fmt.Errorf("missing manifest")
	}
	if part.Header.Get("Content-Type") != "application/json" || part.Header.Get("Content-ID") != "<manifest>" || len(part.Header.Values("Content-ID")) != 1 || len(part.Header.Values("Content-Type")) != 1 || part.Header.Get("Content-Transfer-Encoding") != "" {
		return out, fmt.Errorf("invalid manifest headers")
	}
	raw, err := boundedPart(part, maxManifestBytes)
	if err != nil {
		return out, err
	}
	var manifest transferManifest
	if err = strictJSON(raw, &manifest); err != nil {
		return out, err
	}
	if manifest.Model == "" || len(manifest.Images) == 0 || len(manifest.Images) > 16 {
		return out, fmt.Errorf("invalid generation metadata")
	}
	declaredTotal := 0
	for i, img := range manifest.Images {
		if img.ContentID != fmt.Sprintf("image-%d", i) || img.Bytes <= 0 || img.Bytes > maxImageBytes {
			return out, fmt.Errorf("invalid manifest image")
		}
		declaredTotal += img.Bytes
		if declaredTotal > maxTotalInputBytes {
			return out, fmt.Errorf("images exceed total limit")
		}
	}
	data := make([][]byte, 0, len(manifest.Images))
	for _, img := range manifest.Images {
		part, err = mr.NextRawPart()
		if err != nil {
			return out, fmt.Errorf("missing image part")
		}
		if part.Header.Get("Content-ID") != "<"+img.ContentID+">" || part.Header.Get("Content-Type") != img.MIMEType || len(part.Header.Values("Content-ID")) != 1 || len(part.Header.Values("Content-Type")) != 1 || part.Header.Get("Content-Transfer-Encoding") != "" {
			return out, fmt.Errorf("invalid image part headers")
		}
		b, err := boundedPart(part, int64(img.Bytes))
		if err != nil {
			return out, err
		}
		if len(b) != img.Bytes || imageExtension(img.MIMEType) == "" || http.DetectContentType(b) != img.MIMEType {
			return out, fmt.Errorf("invalid remote image MIME or length")
		}
		data = append(data, b)
	}
	if _, err = mr.NextRawPart(); err != io.EOF {
		return out, fmt.Errorf("unexpected trailing part")
	}
	// Consume any epilogue with the same body bound, including transport truncation.
	if _, err = io.Copy(io.Discard, body); err != nil {
		return out, fmt.Errorf("incomplete response")
	}
	if body.N <= 0 {
		return out, fmt.Errorf("response too large")
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	out = generateOutput{Model: manifest.Model, Notice: manifest.Notice}
	for i, b := range data {
		img := imageMetadata{MIMEType: manifest.Images[i].MIMEType, Bytes: len(b)}
		if f.outputFormat == "base64" || f.outputFormat == "both" {
			img.Base64 = base64.StdEncoding.EncodeToString(b)
			img.data = b
		}
		out.Images = append(out.Images, img)
	}
	if f.outputFormat == "base64" {
		return out, nil
	}
	dir := f.cfg.outputDir
	if err = os.MkdirAll(dir, 0700); err != nil {
		return generateOutput{}, fmt.Errorf("create output directory: %w", err)
	}
	var paths []string
	defer func() {
		if err != nil {
			for _, path := range paths {
				_ = os.Remove(path)
			}
			out = generateOutput{}
		}
	}()
	for i, b := range data {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		var file *os.File
		file, err = os.CreateTemp(dir, "images-mcp-*"+imageExtension(out.Images[i].MIMEType))
		if err != nil {
			return out, err
		}
		paths = append(paths, file.Name())
		_, err = file.Write(b)
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		if err != nil {
			return out, err
		}
		out.Images[i].Path = file.Name()
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	return out, nil
}
