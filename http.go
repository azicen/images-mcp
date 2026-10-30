package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type serverConfig struct {
	Transport string `json:"transport,omitempty" yaml:"transport,omitempty"`
	Listen    string `json:"listen,omitempty" yaml:"listen,omitempty"`
	APIKey    string `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	EnableSSE bool   `json:"enable_sse,omitempty" yaml:"enable_sse,omitempty"`
}

func firstValue(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func (c *config) configureServer(opts cliOptions) error {
	s := &c.file.Server
	s.Transport = strings.ToLower(firstValue(s.Transport, os.Getenv("MCP_TRANSPORT"), "stdio"))
	s.Listen = firstValue(s.Listen, os.Getenv("MCP_LISTEN"), ":8080")
	s.APIKey = firstValue(s.APIKey, os.Getenv("MCP_SERVER_KEY"), os.Getenv("SERVER_API_KEY"))
	if opts.httpSet {
		s.Transport, s.Listen = "http", opts.httpAddress
	}
	if opts.keySet {
		s.APIKey = opts.serverKey
	}
	if s.Transport != "stdio" && s.Transport != "http" {
		return fmt.Errorf("server.transport must be stdio or http")
	}
	return nil
}

func authorize(key string, next http.Handler) http.Handler {
	expected := sha256.Sum256([]byte(key))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := ""
		if fields := strings.Fields(r.Header.Get("Authorization")); len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
			token = fields[1]
		}
		token = firstValue(token, r.Header.Get("X-API-Key"), r.URL.Query().Get("api_key"))
		actual := sha256.Sum256([]byte(token))
		if key != "" && (token == "" || subtle.ConstantTimeCompare(expected[:], actual[:]) != 1) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"unauthorized"}`))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func newHTTPHandler(s *service) http.Handler {
	mux := http.NewServeMux()
	server := newServer(s)
	getServer := func(_ *http.Request) *mcp.Server { return server }
	mux.Handle("/mcp", authorize(s.config.file.Server.APIKey, mcp.NewStreamableHTTPHandler(getServer, &mcp.StreamableHTTPOptions{MaxRequestBodyBytes: 96 << 20})))
	if s.config.file.Server.EnableSSE {
		handler := authorize(s.config.file.Server.APIKey, mcp.NewSSEHandler(getServer, &mcp.SSEOptions{MaxRequestBodyBytes: 96 << 20}))
		mux.Handle("/sse", handler)
		mux.Handle("/sse/", handler)
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	return mux
}

func serveHTTP(ctx context.Context, cfg serverConfig, handler http.Handler) error {
	server := &http.Server{Addr: cfg.Listen, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute, BaseContext: nil}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				_ = server.Close()
			}
		case <-stopped:
		}
	}()
	// A local completion channel also releases the shutdown watcher on listen errors.
	err := server.ListenAndServe()
	close(stopped)
	<-done
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
