package main

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type credentials struct{ baseURL, apiKey string }

type config struct {
	baseURL, apiKey, outputDir string
	gemini, gptImage           credentials
}

func loadConfig() (config, error) {
	c := config{
		baseURL: os.Getenv("OPENAI_BASE_URL"), apiKey: os.Getenv("OPENAI_API_KEY"),
		gemini:    credentials{os.Getenv("GEMINI_BASE_URL"), os.Getenv("GEMINI_API_KEY")},
		gptImage:  credentials{os.Getenv("GPT_IMAGE_BASE_URL"), os.Getenv("GPT_IMAGE_API_KEY")},
		outputDir: os.Getenv("IMAGE_OUTPUT_DIR"),
	}
	if c.outputDir == "" {
		c.outputDir = "images"
	}
	var err error
	c.outputDir, err = filepath.Abs(c.outputDir)
	if err != nil {
		return c, fmt.Errorf("resolve output directory: %w", err)
	}
	return c, nil
}

func (c config) resolveCredentials(in generateInput) (credentials, error) {
	family := credentials{}
	switch {
	case strings.HasPrefix(strings.ToLower(in.Model), "gemini"):
		family = c.gemini
	case strings.HasPrefix(strings.ToLower(in.Model), "gpt-image"):
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
		baseURL: strings.TrimRight(first(in.BaseURL, family.baseURL, c.baseURL), "/"),
		apiKey:  first(in.APIKey, family.apiKey, c.apiKey),
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
	c, err := loadConfig()
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
