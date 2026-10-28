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
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"gopkg.in/yaml.v3"
)

type credentials struct{ baseURL, apiKey string }

type fileCredentials struct {
	BaseURL string `json:"base_url" yaml:"base_url"`
	APIKey  string `json:"api_key" yaml:"api_key"`
}

type fileConfig struct {
	Default   fileCredentials            `json:"default" yaml:"default"`
	Families  map[string]fileCredentials `json:"families" yaml:"families"`
	Models    map[string]fileCredentials `json:"models" yaml:"models"`
	OutputDir string                     `json:"output_dir" yaml:"output_dir"`
}

func parseFlags(args []string, output io.Writer) (string, error) {
	flags := flag.NewFlagSet("danqing", flag.ContinueOnError)
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
		for _, candidate := range []string{"danqing.yaml", "danqing.yml", "danqing.json"} {
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
		c.outputDir = "images"
	}
	c.outputDir, err = filepath.Abs(c.outputDir)
	if err != nil {
		return c, fmt.Errorf("resolve output directory: %w", err)
	}
	return c, nil
}

func (c config) resolveCredentials(in generateInput) (credentials, error) {
	family := credentials{}
	familyName := ""
	model := c.file.Models[in.Model]
	switch {
	case strings.HasPrefix(strings.ToLower(in.Model), "gemini"):
		family = c.gemini
		familyName = "gemini"
	case strings.HasPrefix(strings.ToLower(in.Model), "gpt-image"):
		family = c.gptImage
		familyName = "gpt-image"
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

func newServer(s *service) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "danqing", Version: "0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "generate_image", Description: "Generate or edit images through a unified relay and save them locally. Supports Gemini and GPT Image models."}, func(ctx context.Context, _ *mcp.CallToolRequest, in generateInput) (*mcp.CallToolResult, generateOutput, error) {
		out, err := s.generate(ctx, in)
		return &mcp.CallToolResult{}, out, err
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
		fmt.Fprintln(os.Stderr, "danqing:", err)
		os.Exit(1)
	}
}
