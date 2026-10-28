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

type credentials struct{ baseURL, apiKey string }

type fileCredentials struct {
	API     string `json:"api,omitempty" yaml:"api,omitempty"`
	APIType string `json:"api_type,omitempty" yaml:"api_type,omitempty"`
	BaseURL string `json:"base_url" yaml:"base_url"`
	APIKey  string `json:"api_key" yaml:"api_key"`
}

type fileModel struct {
	API         string `json:"api,omitempty" yaml:"api,omitempty"`
	APIType     string `json:"api_type,omitempty" yaml:"api_type,omitempty"`
	BaseURL     string `json:"base_url" yaml:"base_url"`
	APIKey      string `json:"api_key" yaml:"api_key"`
	Description string `json:"description" yaml:"description"`
	Notes       string `json:"notes,omitempty" yaml:"notes,omitempty"`
}

type fileConfig struct {
	DefaultModel string                     `json:"default_model" yaml:"default_model"`
	Default      fileCredentials            `json:"default" yaml:"default"`
	Families     map[string]fileCredentials `json:"families" yaml:"families"`
	Models       map[string]fileModel       `json:"models" yaml:"models"`
	OutputDir    string                     `json:"output_dir" yaml:"output_dir"`
}

func parseFlags(args []string, output io.Writer) (string, error) {
	flags := flag.NewFlagSet("images-mcp", flag.ContinueOnError)
	flags.SetOutput(output)
	var path string
	flags.StringVar(&path, "config", "", "YAML or JSON configuration file")
	flags.StringVar(&path, "c", "", "alias for -config")
	if err := flags.Parse(args); err != nil {
		return "", err
	}
	if flags.NArg() != 0 {
		return "", fmt.Errorf("unexpected positional arguments")
	}
	var explicit bool
	flags.Visit(func(f *flag.Flag) { explicit = true })
	if explicit && strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("configuration path must not be empty")
	}
	return path, nil
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
	return c, nil
}

type config struct {
	baseURL, apiKey, outputDir string
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
	for name := range file.Models {
		if family, err := (config{file: file}).modelFamily(generateInput{Model: name}); err != nil {
			return config{}, fmt.Errorf("model %q: %w", name, err)
		} else if family == "" {
			return config{}, fmt.Errorf("model %q: configure api or api_type for an unrecognized model prefix", name)
		}
	}
	c := config{
		file:    file,
		baseURL: os.Getenv("OPENAI_BASE_URL"), apiKey: os.Getenv("OPENAI_API_KEY"),
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
	b.WriteString("Generate or edit images through a unified relay and save them locally.\nDefault model: ")
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

func newServer(s *service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "images-mcp", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "generate_image", Description: s.config.generationDescription()}, func(ctx context.Context, _ *mcp.CallToolRequest, in generateInput) (*mcp.CallToolResult, generateOutput, error) {
		out, err := s.generate(ctx, in)
		return &mcp.CallToolResult{}, out, err
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
	return server
}

func run() error {
	path, err := parseFlags(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return nil
	}
	if err != nil {
		return err
	}
	c, err := loadConfig(path)
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 5 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
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
	err = newServer(&service{config: c, client: client}).Run(ctx, &mcp.StdioTransport{})
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
