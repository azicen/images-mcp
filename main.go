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

type config struct{ baseURL, apiKey, outputDir string }

func loadConfig() (config, error) {
	c := config{baseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("OPENAI_BASE_URL")), "/"), apiKey: strings.TrimSpace(os.Getenv("OPENAI_API_KEY")), outputDir: os.Getenv("IMAGE_OUTPUT_DIR")}
	u, err := url.Parse(c.baseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return c, fmt.Errorf("OPENAI_BASE_URL must be an HTTP(S) URL without credentials, query, or fragment")
	}
	if c.apiKey == "" {
		return c, fmt.Errorf("OPENAI_API_KEY is required")
	}
	if !strings.HasSuffix(c.baseURL, "/v1") {
		c.baseURL += "/v1"
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
