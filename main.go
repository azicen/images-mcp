package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

type generateInput struct {
	Timeout     string   `json:"timeout,omitempty" jsonschema:"Optional request timeout duration (e.g. 10m, 15m, 600s); overrides model and config defaults"`
	API         string   `json:"api,omitempty" jsonschema:"Optional API route: gemini or chat; gpt, gpt-image or images; overrides model configuration"`
	APIType     string   `json:"api_type,omitempty" jsonschema:"Alias for api"`
	BaseURL     string   `json:"base_url,omitempty" jsonschema:"Optional relay base URL; overrides model family and OPENAI_BASE_URL defaults"`
	APIKey      string   `json:"api_key,omitempty" jsonschema:"Optional relay API key; overrides model family and OPENAI_API_KEY defaults"`
	Prompt      string   `json:"prompt" jsonschema:"Generation or editing instructions"`
	Model       string   `json:"model,omitempty" jsonschema:"Optional relay model name; defaults to configured default_model. See tool description and list_models for available models."`
	InputImages []string `json:"input_images,omitempty" jsonschema:"Input images: local paths, HTTP(S) URLs, Data URIs, or raw Base64; one image for editing, multiple images use the first as primary and the rest as references; max 16 images, PNG/JPEG/WebP only, 32 MiB each and 64 MiB total"`
	Size        string   `json:"size,omitempty" jsonschema:"Optional GPT Image size"`
	Quality     string   `json:"quality,omitempty" jsonschema:"Optional GPT Image quality"`
}

// normalizedInputImages preserves the primary image and original parameter indices.
func (in generateInput) normalizedInputImages() ([]inputImageSource, error) {
	var sources []inputImageSource
	for i, ref := range in.InputImages {
		if strings.TrimSpace(ref) != "" {
			sources = append(sources, inputImageSource{ref: ref, source: fmt.Sprintf("input_images[%d]", i)})
		}
	}
	if len(sources) > 16 {
		return nil, fmt.Errorf("input images exceed maximum of 16 (got %d)", len(sources))
	}
	return sources, nil
}

type credentials struct{ baseURL, apiKey string }

type fileCredentials struct {
	OutputFormat string `json:"output_format,omitempty" yaml:"output_format,omitempty"`
	Timeout      string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	API          string `json:"api,omitempty" yaml:"api,omitempty"`
	APIType      string `json:"api_type,omitempty" yaml:"api_type,omitempty"`
	BaseURL      string `json:"base_url" yaml:"base_url"`
	APIKey       string `json:"api_key" yaml:"api_key"`
}

type fileModel struct {
	Timeout     string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	API         string `json:"api,omitempty" yaml:"api,omitempty"`
	APIType     string `json:"api_type,omitempty" yaml:"api_type,omitempty"`
	BaseURL     string `json:"base_url" yaml:"base_url"`
	APIKey      string `json:"api_key" yaml:"api_key"`
	Description string `json:"description" yaml:"description"`
	Notes       string `json:"notes,omitempty" yaml:"notes,omitempty"`
}

type fileConfig struct {
	Server       serverConfig               `json:"server,omitempty" yaml:"server,omitempty"`
	S3           s3Config                   `json:"s3,omitempty" yaml:"s3,omitempty"`
	OutputFormat string                     `json:"output_format,omitempty" yaml:"output_format,omitempty"`
	Timeout      string                     `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	DefaultModel string                     `json:"default_model" yaml:"default_model"`
	Default      fileCredentials            `json:"default" yaml:"default"`
	Families     map[string]fileCredentials `json:"families" yaml:"families"`
	Models       map[string]fileModel       `json:"models" yaml:"models"`
	OutputDir    string                     `json:"output_dir" yaml:"output_dir"`
}

type cliOptions struct {
	path, httpAddress, serverKey string
	httpSet, keySet              bool
}

func parseFlags(args []string, output io.Writer) (string, error) {
	opts, err := parseCLI(args, output)
	return opts.path, err
}

func parseCLI(args []string, output io.Writer) (cliOptions, error) {
	var opts cliOptions
	flags := flag.NewFlagSet("images-mcp", flag.ContinueOnError)
	flags.SetOutput(output)
	path := &opts.path
	flags.StringVar(&opts.httpAddress, "http", "", "enable HTTP MCP at this listen address")
	flags.StringVar(&opts.serverKey, "server-key", "", "HTTP service access key")
	flags.StringVar(path, "config", "", "YAML or JSON configuration file")
	flags.StringVar(path, "c", "", "alias for -config")
	if err := flags.Parse(args); err != nil {
		return opts, err
	}
	if flags.NArg() != 0 {
		return opts, fmt.Errorf("unexpected positional arguments")
	}
	var configSet bool
	flags.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "config", "c":
			configSet = true
		case "http":
			opts.httpSet = true
		case "server-key":
			opts.keySet = true
		}
	})
	if configSet && strings.TrimSpace(opts.path) == "" {
		opts.path = ""
		return opts, fmt.Errorf("configuration path must not be empty")
	}
	if opts.httpSet && strings.TrimSpace(opts.httpAddress) == "" {
		return opts, fmt.Errorf("HTTP listen address must not be empty")
	}
	return opts, nil
}

func loadFileConfig(path string) (fileConfig, error) {
	var c fileConfig
	if path == "" {
		for _, candidate := range []string{"images-mcp.yaml", "images-mcp.yml", "images-mcp.json"} {
			_, err := os.Stat(candidate)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return c, fmt.Errorf("inspect configuration %q: %w", candidate, err)
			}
			path = candidate
			break
		}
		if path == "" {
			return c, nil
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return c, fmt.Errorf("open configuration %q: %w", path, err)
	}
	defer f.Close()
	b, err := readLimited(f, 1<<20)
	if err != nil {
		return c, fmt.Errorf("read configuration %q: %w", path, err)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		err = json.Unmarshal(b, &c)
	case ".yaml", ".yml":
		decoder := yaml.NewDecoder(strings.NewReader(string(b)))
		err = decoder.Decode(&c)
		if errors.Is(err, io.EOF) {
			err = nil
		}
		if err == nil {
			var extra any
			if next := decoder.Decode(&extra); next != io.EOF {
				err = fmt.Errorf("expected a single YAML document")
			}
		}
	default:
		return c, fmt.Errorf("configuration %q must use .yaml, .yml, or .json", path)
	}
	if err != nil {
		return fileConfig{}, fmt.Errorf("invalid configuration %q", path)
	}
	var presence struct {
		S3 map[string]any `yaml:"s3"`
	}
	if yaml.Unmarshal(b, &presence) == nil {
		_, c.S3.pathStyleSet = presence.S3["use_path_style"]
	}
	return c, nil
}

type config struct {
	baseURL, apiKey, outputDir string
	outputFormat               string
	gemini, gptImage           credentials
	file                       fileConfig
}

func loadConfig(paths ...string) (config, error) {
	var path string
	if len(paths) > 0 {
		path = paths[0]
	}
	file, err := loadFileConfig(path)
	if err != nil {
		return config{}, err
	}
	for _, setting := range []struct{ name, value string }{
		{"timeout", file.Timeout}, {"default.timeout", file.Default.Timeout},
	} {
		if setting.value != "" {
			if _, err := parseTimeout(setting.value); err != nil {
				return config{}, fmt.Errorf("%s: %w", setting.name, err)
			}
		}
	}
	for name, model := range file.Models {
		if model.Timeout != "" {
			if _, err := parseTimeout(model.Timeout); err != nil {
				return config{}, fmt.Errorf("model %q timeout: %w", name, err)
			}
		}
		if family, err := (config{file: file}).modelFamily(generateInput{Model: name}); err != nil {
			return config{}, fmt.Errorf("model %q: %w", name, err)
		} else if family == "" {
			return config{}, fmt.Errorf("model %q: configure api or api_type for an unrecognized model prefix", name)
		}
	}
	outputFormat := "file"
	for _, setting := range []struct{ name, value string }{
		{"output_format", file.OutputFormat},
		{"default.output_format", file.Default.OutputFormat},
		{"IMAGE_OUTPUT_FORMAT", os.Getenv("IMAGE_OUTPUT_FORMAT")},
	} {
		if setting.value == "" {
			continue
		}
		value := strings.ToLower(setting.value)
		switch value {
		case "file", "base64", "both":
		default:
			return config{}, fmt.Errorf("%s: expected file, base64, or both", setting.name)
		}
	}
	for _, value := range []string{file.OutputFormat, file.Default.OutputFormat, os.Getenv("IMAGE_OUTPUT_FORMAT")} {
		if value != "" {
			outputFormat = strings.ToLower(value)
			break
		}
	}
	c := config{
		file:         file,
		outputFormat: outputFormat,
		baseURL:      os.Getenv("OPENAI_BASE_URL"), apiKey: os.Getenv("OPENAI_API_KEY"),
		gemini:    credentials{os.Getenv("GEMINI_BASE_URL"), os.Getenv("GEMINI_API_KEY")},
		gptImage:  credentials{os.Getenv("GPT_IMAGE_BASE_URL"), os.Getenv("GPT_IMAGE_API_KEY")},
		outputDir: os.Getenv("IMAGE_OUTPUT_DIR"),
	}
	if file.OutputDir != "" {
		c.outputDir = file.OutputDir
	}
	if c.outputDir == "" {
		c.outputDir = os.TempDir()
	}
	c.outputDir, err = filepath.Abs(c.outputDir)
	if err != nil {
		return c, fmt.Errorf("resolve output directory: %w", err)
	}
	return c, nil
}

// parseTimeout accepts Go durations and integer seconds without overflowing.
func parseTimeout(s string) (time.Duration, error) {
	value := strings.TrimSpace(s)
	if value != "" && strings.IndexFunc(value, func(r rune) bool { return r < '0' || r > '9' }) == -1 {
		value += "s"
	}
	duration, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid timeout %q: expected a positive Go duration (e.g. 10m) or integer seconds: %w", s, err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("invalid timeout %q: must be greater than zero", s)
	}
	return duration, nil
}

func (c config) resolveTimeout(in generateInput) (time.Duration, error) {
	model := in.Model
	if model == "" {
		model = c.file.DefaultModel
	}
	for _, setting := range []struct{ name, value string }{
		{"request timeout", in.Timeout},
		{"model timeout", c.file.Models[model].Timeout},
		{"default.timeout", c.file.Default.Timeout},
		{"timeout", c.file.Timeout},
		{"IMAGE_TIMEOUT", os.Getenv("IMAGE_TIMEOUT")},
	} {
		if setting.value != "" {
			duration, err := parseTimeout(setting.value)
			if err != nil {
				return 0, fmt.Errorf("%s: %w", setting.name, err)
			}
			return duration, nil
		}
	}
	return 10 * time.Minute, nil
}

func (c config) resolveCredentials(in generateInput) (credentials, error) {
	family := credentials{}
	model := c.file.Models[in.Model]
	familyName, err := c.modelFamily(in)
	if err != nil {
		return credentials{}, err
	}
	switch familyName {
	case "gemini":
		family = c.gemini
	case "gpt-image":
		family = c.gptImage
	}
	first := func(values ...string) string {
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				return value
			}
		}
		return ""
	}
	resolved := credentials{
		baseURL: strings.TrimRight(first(in.BaseURL, model.BaseURL, c.file.Families[familyName].BaseURL, family.baseURL, c.file.Default.BaseURL, c.baseURL), "/"),
		apiKey:  first(in.APIKey, model.APIKey, c.file.Families[familyName].APIKey, family.apiKey, c.file.Default.APIKey, c.apiKey),
	}
	u, err := url.Parse(resolved.baseURL)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(resolved.baseURL, "#") {
		return credentials{}, fmt.Errorf("resolved base_url must be an HTTP(S) URL without credentials, query, or fragment")
	}
	if resolved.apiKey == "" || strings.ContainsAny(resolved.apiKey, " \t\r\n") || strings.IndexFunc(resolved.apiKey, func(r rune) bool { return r < 0x21 || r > 0x7e }) >= 0 {
		return credentials{}, fmt.Errorf("resolved api_key must be a nonempty printable ASCII token without whitespace")
	}
	if !strings.HasSuffix(resolved.baseURL, "/v1") {
		resolved.baseURL += "/v1"
	}
	return resolved, nil
}

type modelRecord struct {
	ID           string `json:"id"`
	Description  string `json:"description"`
	Notes        string `json:"notes,omitempty"`
	SupportsEdit bool   `json:"supports_edit"`
	IsDefault    bool   `json:"is_default"`
}

func (c config) modelIDs() []string {
	ids := make([]string, 0, len(c.file.Models))
	for id := range c.file.Models {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (c config) listModels() ([]modelRecord, error) {
	records := make([]modelRecord, 0, len(c.file.Models))
	for _, id := range c.modelIDs() {
		model := c.file.Models[id]
		family, err := c.modelFamily(generateInput{Model: id})
		if err != nil {
			return nil, err
		}
		if family != "gpt-image" && family != "gemini" {
			return nil, fmt.Errorf("model %q: configure api or api_type", id)
		}
		records = append(records, modelRecord{ID: id, Description: model.Description, Notes: model.Notes, SupportsEdit: true, IsDefault: id == c.file.DefaultModel})
	}
	return records, nil
}

func (c config) generationDescription() string {
	var b strings.Builder
	b.WriteString("Generate or edit images through a unified relay. Output follows the global output_format configuration. Supply input_images for up to 16 ordered images: first is the primary image to edit, others are style/context references. Inputs: PNG/JPEG/WebP, 32 MiB each, 64 MiB total.\nDefault model: ")
	if c.file.DefaultModel == "" {
		b.WriteString("none (supply model)")
	} else {
		b.WriteString(c.file.DefaultModel)
	}
	b.WriteString(".\nConfigured models (alphabetical):")
	for _, id := range c.modelIDs() {
		fmt.Fprintf(&b, "\n- %s: %s", id, c.file.Models[id].Description)
	}
	if len(c.file.Models) == 0 {
		b.WriteString(" none")
	}
	b.WriteString("\nCall list_models for complete details and guidelines.")
	return b.String()
}

type uploadImageInput struct {
	Filename string `json:"filename,omitempty" jsonschema:"Optional original filename of the local image (e.g. cat.png)"`
}

func (s *service) uploadInstructions() string {
	instructions := `HTTP mode: upload local reference images to POST /upload using multipart/form-data (field: file or image) or a binary image body. Supported formats: PNG/JPEG/WebP; maximum 32 MiB per image.
Do not generate large Base64 tool arguments. Run curl on the machine containing the local file, replacing the placeholders with your server URL and local path:
curl -X POST "<server_url>/upload" -F "file=@<local_path>"
A successful upload returns {"url":"https://...","mime_type":"image/png","bytes":1048576}. Put the returned url in generate_image input_images.
S3 storage must be configured for uploads; uploads are not automatically retried.`
	if s.config.file.Server.APIKey != "" {
		instructions += `
Authentication is required: Authorization: Bearer <server_api_key> or X-API-Key. Use the server key, not the relay key:
curl -X POST "<server_url>/upload" -H "Authorization: Bearer <server_api_key>" -F "file=@<local_path>"`
	}
	if s.storage == nil {
		instructions += "\nS3 storage is currently unavailable; configure S3 and restart the server before uploading."
	}
	return instructions
}

func newServer(s *service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "images-mcp", Version: "0.3.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "generate_image", Description: s.config.generationDescription()}, func(ctx context.Context, _ *mcp.CallToolRequest, in generateInput) (*mcp.CallToolResult, generateOutput, error) {
		out, err := s.generate(ctx, in)
		result := &mcp.CallToolResult{}
		if err == nil {
			if s.config.outputFormat == "base64" || s.config.outputFormat == "both" {
				data, marshalErr := json.Marshal(out)
				if marshalErr != nil {
					return nil, out, marshalErr
				}
				result.Content = append(result.Content, &mcp.TextContent{Text: string(data)})
			}
			for _, img := range out.Images {
				if img.Base64 != "" {
					result.Content = append(result.Content, &mcp.ImageContent{Data: img.data, MIMEType: img.MIMEType})
				}
			}
		}
		return result, out, err
	})
	mcp.AddTool(server, &mcp.Tool{Name: "list_models", Description: "List available image generation models, their capabilities, and guidelines."}, func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, any, error) {
		records, err := s.config.listModels()
		if err != nil {
			return nil, nil, err
		}
		data, err := json.Marshal(records)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(data)}}}, nil, nil
	})
	if s.config.file.Server.Transport == "http" {
		mcp.AddTool(server, &mcp.Tool{Name: "upload_image", Description: "Get instructions and endpoint details for uploading local reference images to S3 when using images-mcp over HTTP. Call this before generate_image if you have local image files."}, func(_ context.Context, _ *mcp.CallToolRequest, _ uploadImageInput) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s.uploadInstructions()}}}, nil, nil
		})
	}
	return server
}

func run() error {
	opts, err := parseCLI(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	c, err := loadConfig(opts.path)
	if err != nil {
		return err
	}
	if err := c.configureServer(opts); err != nil {
		return err
	}
	store, err := newObjectStorage(c)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 0, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return fmt.Errorf("too many redirects")
		}
		if len(via) > 0 && via[0].Header.Get("Authorization") != "" && (req.URL.Scheme != via[0].URL.Scheme || req.URL.Host != via[0].URL.Host) {
			return fmt.Errorf("relay redirect changed origin")
		}
		return nil
	}}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	svc := &service{config: c, client: client, storage: store}
	if c.file.Server.Transport == "http" {
		return serveHTTP(ctx, c.file.Server, newHTTPHandler(svc))
	}
	err = newServer(svc).Run(ctx, &mcp.StdioTransport{})
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "images-mcp:", err)
		os.Exit(1)
	}
}
