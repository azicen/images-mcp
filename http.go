package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"strings"
	"time"
)

type serverConfig struct {
	Listen string `json:"listen,omitempty" yaml:"listen,omitempty"`
	APIKey string `json:"api_key,omitempty" yaml:"api_key,omitempty"`
}
type forwardConfig struct {
	URL     string `json:"url,omitempty" yaml:"url,omitempty"`
	APIKey  string `json:"api_key,omitempty" yaml:"api_key,omitempty"`
	Timeout string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

func firstValue(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
func (c *config) configureServer(opts cliOptions) error {
	choose := func(name, cli, file string, fallback ...string) string {
		if opts.set[name] {
			return cli
		}
		return firstValue(append([]string{file}, fallback...)...)
	}
	c.file.Mode = choose("mode", opts.mode, c.file.Mode, os.Getenv("IMAGES_MCP_MODE"), "standalone")
	s := &c.file.Server
	s.Listen = choose("listen", opts.listen, s.Listen, os.Getenv("MCP_LISTEN"), ":8080")
	s.APIKey = choose("server-key", opts.serverKey, s.APIKey, os.Getenv("MCP_SERVER_KEY"), os.Getenv("SERVER_API_KEY"))
	f := &c.file.Forward
	f.URL = strings.TrimRight(choose("forward-url", opts.forwardURL, f.URL, os.Getenv("IMAGES_MCP_FORWARD_URL")), "/")
	f.APIKey = choose("forward-key", opts.forwardKey, f.APIKey, os.Getenv("IMAGES_MCP_FORWARD_KEY"), os.Getenv("FORWARD_KEY"), os.Getenv("MCP_FORWARD_KEY"))
	f.Timeout = choose("forward-timeout", opts.forwardTimeout, f.Timeout, os.Getenv("IMAGES_MCP_FORWARD_TIMEOUT"), "15m")
	switch c.file.Mode {
	case "standalone":
	case "server":
		if strings.TrimSpace(s.Listen) == "" {
			return fmt.Errorf("listen must not be empty")
		}
	case "forward":
		if err := validateStorageURL(f.URL); err != nil {
			return fmt.Errorf("forward.url: %w", err)
		}
		if _, err := parseTimeout(f.Timeout); err != nil {
			return err
		}
	default:
		return fmt.Errorf("mode must be standalone, server, or forward")
	}
	return nil
}
func respond(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func failHTTP(w http.ResponseWriter, status int, err error) {
	respond(w, status, map[string]string{"error": err.Error()})
}
func authorize(key string, next http.Handler) http.Handler {
	expected := sha256.Sum256([]byte(key))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-API-Key")
		if fields := strings.Fields(r.Header.Get("Authorization")); len(fields) == 2 && strings.EqualFold(fields[0], "Bearer") {
			token = fields[1]
		}
		actual := sha256.Sum256([]byte(token))
		if key != "" && subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
			failHTTP(w, 401, fmt.Errorf("unauthorized"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
func method(w http.ResponseWriter, r *http.Request, want string) bool {
	if r.Method == want {
		return true
	}
	w.Header().Set("Allow", want)
	failHTTP(w, 405, fmt.Errorf("method must be %s", want))
	return false
}
func newHTTPHandler(s *service) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if method(w, r, "GET") {
			respond(w, 200, map[string]string{"status": "ok"})
		}
	})
	mux.Handle("/v1/models", authorize(s.config.file.Server.APIKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !method(w, r, "GET") {
			return
		}
		models, err := s.config.listModels()
		if err != nil {
			failHTTP(w, 500, err)
			return
		}
		respond(w, 200, models)
	})))
	mux.Handle("/v1/generate", authorize(s.config.file.Server.APIKey, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !method(w, r, "POST") {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 96<<20)
		defer r.Body.Close()
		in, err := decodeREST(r)
		if err == nil {
			in, err = s.config.defaultInput(in)
		}
		if err == nil {
			_, err = s.config.route(in)
		}
		var budget time.Duration
		if err == nil {
			budget, err = s.config.resolveTimeout(in)
		}
		if err != nil {
			status := 400
			var size *http.MaxBytesError
			if errors.As(err, &size) || errors.Is(err, errInputSize) {
				status = 413
			}
			failHTTP(w, status, err)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), budget)
		defer cancel()
		loaded, loadErr := s.loadInputImages(ctx, in)
		if loadErr != nil {
			status := 400
			if errors.Is(loadErr, errInputSize) || strings.Contains(loadErr.Error(), "exceed") {
				status = 413
			}
			failHTTP(w, status, loadErr)
			return
		}
		sources, _ := in.normalizedInputImages()
		in.prepared = make(map[string]inputImage, len(sources))
		for i, source := range sources {
			in.prepared[source.ref] = loaded[i]
		}
		out, err := s.generate(ctx, in)
		if err != nil {
			failHTTP(w, 500, err)
			return
		}
		respond(w, 200, out)
	})))
	return mux
}

var errInputSize = errors.New("input exceeds size limit")

// restInput deliberately excludes relay credentials and protocol overrides.
type restInput struct {
	Prompt      string   `json:"prompt"`
	Model       string   `json:"model,omitempty"`
	Size        string   `json:"size,omitempty"`
	Quality     string   `json:"quality,omitempty"`
	Timeout     string   `json:"timeout,omitempty"`
	InputImages []string `json:"input_images,omitempty"`
}

func publicInput(in generateInput) restInput {
	return restInput{in.Prompt, in.Model, in.Size, in.Quality, in.Timeout, in.InputImages}
}
func decodePublic(reader io.Reader) (generateInput, error) {
	var in restInput
	d := json.NewDecoder(reader)
	d.DisallowUnknownFields()
	if err := d.Decode(&in); err != nil {
		return generateInput{}, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return generateInput{}, fmt.Errorf("expected one JSON object")
	}
	return generateInput{Prompt: in.Prompt, Model: in.Model, Size: in.Size, Quality: in.Quality, Timeout: in.Timeout, InputImages: in.InputImages}, nil
}
func boundedPart(r io.Reader, limit int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, errInputSize
	}
	return b, nil
}
func decodeREST(r *http.Request) (generateInput, error) {
	var in generateInput
	media, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return in, err
	}
	files := map[string]inputImage{}
	switch media {
	case "application/json":
		in, err = decodePublic(r.Body)
	case "multipart/form-data":
		mr, e := r.MultipartReader()
		if e != nil {
			return in, e
		}
		fields := map[string]string{}
		total := 0
		fieldBytes := 0
		for {
			part, e := mr.NextPart()
			if e == io.EOF {
				break
			}
			if e != nil {
				return in, e
			}
			name := part.FormName()
			if name == "" || len(name) > 128 {
				return in, fmt.Errorf("missing part name")
			}
			if _, ok := fields[name]; ok {
				return in, fmt.Errorf("duplicate field %q", name)
			}
			if _, ok := files[name]; ok {
				return in, fmt.Errorf("duplicate file %q", name)
			}
			if part.FileName() != "" {
				if len(files) >= 16 {
					return in, fmt.Errorf("at most 16 files")
				}
				b, e := boundedPart(part, maxImageBytes)
				if e != nil {
					return in, e
				}
				total += len(b)
				if total > maxTotalInputBytes {
					return in, errInputSize
				}
				mt := http.DetectContentType(b)
				if mt != "image/png" && mt != "image/jpeg" && mt != "image/webp" {
					return in, fmt.Errorf("unsupported input image MIME")
				}
				files[name] = inputImage{b, mt}
			} else {
				switch name {
				case "request", "prompt", "model", "size", "quality", "timeout", "input_images":
				default:
					return in, fmt.Errorf("unknown multipart field %q", name)
				}
				b, e := boundedPart(part, 1<<20)
				if e != nil {
					return in, e
				}
				fieldBytes += len(b)
				if fieldBytes > 1<<20 {
					return in, errInputSize
				}
				fields[name] = string(b)
			}
			if e := part.Close(); e != nil {
				return in, e
			}
		}
		if len(fields) == 0 {
			return in, fmt.Errorf("missing multipart request fields")
		}
		if raw, ok := fields["request"]; ok {
			if len(fields) != 1 {
				return in, fmt.Errorf("request cannot be mixed with individual fields")
			}
			in, err = decodePublic(strings.NewReader(raw))
		} else {
			obj := map[string]any{}
			for k, v := range fields {
				if k == "input_images" {
					var refs []string
					if e := json.Unmarshal([]byte(v), &refs); e != nil {
						return in, e
					}
					obj[k] = refs
				} else {
					obj[k] = v
				}
			}
			b, _ := json.Marshal(obj)
			in, err = decodePublic(strings.NewReader(string(b)))
		}
	default:
		return in, fmt.Errorf("expected application/json or multipart/form-data")
	}
	if err != nil {
		return in, err
	}
	if len(in.InputImages) > 16 {
		return in, fmt.Errorf("at most 16 input images")
	}
	used := map[string]bool{}
	for _, ref := range in.InputImages {
		if strings.HasPrefix(ref, "part:") {
			name := strings.TrimPrefix(ref, "part:")
			if _, ok := files[name]; !ok {
				return in, fmt.Errorf("missing file %q", name)
			}
			used[name] = true
			continue
		}
		if !strings.HasPrefix(ref, "https://") && !strings.HasPrefix(ref, "http://") && !strings.HasPrefix(ref, "data:") {
			return in, fmt.Errorf("local paths are not accepted by REST; use forward mode")
		}
	}
	if len(used) != len(files) {
		return in, fmt.Errorf("unreferenced multipart file")
	}
	in.prepared = map[string]inputImage{}
	for name, img := range files {
		in.prepared["part:"+name] = img
	}
	return in, nil
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
