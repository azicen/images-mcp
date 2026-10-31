package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type s3Config struct {
	pathStyleSet    bool
	Endpoint        string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	Bucket          string `json:"bucket,omitempty" yaml:"bucket,omitempty"`
	Region          string `json:"region,omitempty" yaml:"region,omitempty"`
	AccessKeyID     string `json:"access_key_id,omitempty" yaml:"access_key_id,omitempty"`
	SecretAccessKey string `json:"secret_access_key,omitempty" yaml:"secret_access_key,omitempty"`
	SessionToken    string `json:"session_token,omitempty" yaml:"session_token,omitempty"`
	UsePathStyle    bool   `json:"use_path_style,omitempty" yaml:"use_path_style,omitempty"`
	KeyPrefix       string `json:"key_prefix,omitempty" yaml:"key_prefix,omitempty"`
	PublicURLPrefix string `json:"public_url_prefix,omitempty" yaml:"public_url_prefix,omitempty"`
	PresignTTL      string `json:"presign_ttl,omitempty" yaml:"presign_ttl,omitempty"`
}

type objectStorage struct {
	client *s3.Client
	cfg    s3Config
	ttl    time.Duration
}

func validateStorageURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(value, "#") {
		return fmt.Errorf("expected HTTP(S) URL without credentials, query, or fragment")
	}
	return nil
}

func newObjectStorage(c config) (*objectStorage, error) {
	if c.file.Mode != "server" || c.outputFormat == "base64" {
		return nil, nil
	}
	cfg := c.file.S3
	cfg.Endpoint = firstValue(cfg.Endpoint, os.Getenv("S3_ENDPOINT"), os.Getenv("AWS_ENDPOINT_URL_S3"))
	cfg.Bucket = firstValue(cfg.Bucket, os.Getenv("S3_BUCKET"))
	cfg.Region = firstValue(cfg.Region, os.Getenv("S3_REGION"), os.Getenv("AWS_REGION"), "us-east-1")
	cfg.AccessKeyID = firstValue(cfg.AccessKeyID, os.Getenv("AWS_ACCESS_KEY_ID"))
	cfg.SecretAccessKey = firstValue(cfg.SecretAccessKey, os.Getenv("AWS_SECRET_ACCESS_KEY"))
	cfg.SessionToken = firstValue(cfg.SessionToken, os.Getenv("AWS_SESSION_TOKEN"))
	cfg.PublicURLPrefix = firstValue(cfg.PublicURLPrefix, os.Getenv("S3_PUBLIC_URL_PREFIX"))
	cfg.KeyPrefix = strings.Trim(firstValue(cfg.KeyPrefix, "images"), "/")
	cfg.PresignTTL = firstValue(cfg.PresignTTL, "24h")
	if !cfg.pathStyleSet && !cfg.UsePathStyle && os.Getenv("S3_USE_PATH_STYLE") != "" {
		value, err := strconv.ParseBool(os.Getenv("S3_USE_PATH_STYLE"))
		if err != nil {
			return nil, fmt.Errorf("invalid S3_USE_PATH_STYLE")
		}
		cfg.UsePathStyle = value
	}
	if strings.TrimSpace(cfg.Bucket) == "" || strings.TrimSpace(cfg.AccessKeyID) == "" || strings.TrimSpace(cfg.SecretAccessKey) == "" {
		return nil, fmt.Errorf("HTTP file/both output requires S3 bucket and complete static credentials")
	}
	for name, value := range map[string]string{"s3.endpoint": cfg.Endpoint, "s3.public_url_prefix": cfg.PublicURLPrefix} {
		if value != "" {
			if err := validateStorageURL(value); err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
		}
	}
	ttl, err := time.ParseDuration(cfg.PresignTTL)
	if err != nil || ttl <= 0 || ttl > 7*24*time.Hour {
		return nil, fmt.Errorf("s3.presign_ttl must be positive and at most 168h")
	}
	client := s3.NewFromConfig(aws.Config{Region: cfg.Region, Credentials: awscredentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, cfg.SessionToken), RetryMaxAttempts: 1}, func(o *s3.Options) {
		o.UsePathStyle = cfg.UsePathStyle
		if cfg.Endpoint != "" {
			o.BaseEndpoint = aws.String(cfg.Endpoint)
		}
	})
	return &objectStorage{client: client, cfg: cfg, ttl: ttl}, nil
}

func (s *objectStorage) remove(ctx context.Context, key string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key)})
	if err != nil {
		return fmt.Errorf("delete S3 image: %w", err)
	}
	return nil
}

func (s *objectStorage) upload(ctx context.Context, data []byte, mimeType string) (string, string, error) {
	suffix := make([]byte, 16)
	if _, err := rand.Read(suffix); err != nil {
		return "", "", fmt.Errorf("generate object key: %w", err)
	}
	key := strings.TrimPrefix(s.cfg.KeyPrefix+"/"+time.Now().UTC().Format("2006/01/02")+"/"+hex.EncodeToString(suffix)+imageExtension(mimeType), "/")
	_, err := s.client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key), Body: bytes.NewReader(data), ContentType: aws.String(mimeType)})
	if err != nil {
		return "", "", fmt.Errorf("upload S3 image: %w", err)
	}
	if s.cfg.PublicURLPrefix != "" {
		u, _ := url.Parse(strings.TrimRight(s.cfg.PublicURLPrefix, "/"))
		u.Path = strings.TrimRight(u.Path, "/") + "/" + key
		u.RawPath = ""
		return u.String(), key, nil
	}
	signed, err := s3.NewPresignClient(s.client).PresignGetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.cfg.Bucket), Key: aws.String(key)}, func(o *s3.PresignOptions) { o.Expires = s.ttl })
	if err != nil {
		return "", "", errors.Join(fmt.Errorf("presign S3 image: %w", err), s.remove(context.WithoutCancel(ctx), key))
	}
	return signed.URL, key, nil
}
