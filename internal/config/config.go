package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr               string
	DataDir            string
	BaseURL            string
	CookieSecure       bool
	AdminUsername      string
	AdminPassword      string
	S3Endpoint         string
	S3PublicEndpoint   string
	S3Region           string
	S3Bucket           string
	S3AccessKey        string
	S3SecretKey        string
	S3PathStyle        bool
	PresignExpires     time.Duration
	MultipartThreshold int64
	UploadExpires      time.Duration
	PartSize           int64
}

func Load() (Config, error) {
	c := Config{
		Addr:             env("APP_ADDR", ":8080"),
		DataDir:          env("APP_DATA_DIR", "/data"),
		BaseURL:          strings.TrimRight(env("APP_BASE_URL", "http://localhost:8080"), "/"),
		AdminUsername:    os.Getenv("ADMIN_USERNAME"),
		AdminPassword:    os.Getenv("ADMIN_PASSWORD"),
		S3Endpoint:       strings.TrimRight(os.Getenv("S3_ENDPOINT"), "/"),
		S3PublicEndpoint: strings.TrimRight(os.Getenv("S3_PUBLIC_ENDPOINT"), "/"),
		S3Region:         env("S3_REGION", "us-east-1"),
		S3Bucket:         os.Getenv("S3_BUCKET"),
		S3AccessKey:      os.Getenv("S3_ACCESS_KEY"),
		S3SecretKey:      os.Getenv("S3_SECRET_KEY"),
	}
	var err error
	if c.CookieSecure, err = boolEnv("COOKIE_SECURE", strings.HasPrefix(c.BaseURL, "https://")); err != nil {
		return c, err
	}
	if c.S3PathStyle, err = boolEnv("S3_PATH_STYLE", false); err != nil {
		return c, err
	}
	if c.PresignExpires, err = durationEnv("PRESIGN_EXPIRES", 15*time.Minute); err != nil {
		return c, err
	}
	if c.UploadExpires, err = durationEnv("UPLOAD_EXPIRES", 24*time.Hour); err != nil {
		return c, err
	}
	if c.MultipartThreshold, err = int64Env("MULTIPART_THRESHOLD", 100*1024*1024); err != nil {
		return c, err
	}
	if c.PartSize, err = int64Env("PART_SIZE", 16*1024*1024); err != nil {
		return c, err
	}
	if c.MultipartThreshold <= 0 {
		return c, errors.New("MULTIPART_THRESHOLD must be positive")
	}
	if c.PartSize < 5*1024*1024 {
		return c, errors.New("PART_SIZE must be at least 5 MiB")
	}
	base, err := url.Parse(c.BaseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") {
		return c, errors.New("APP_BASE_URL must be an absolute http(s) URL")
	}
	if c.S3Bucket == "" || c.S3AccessKey == "" || c.S3SecretKey == "" {
		return c, errors.New("S3_BUCKET, S3_ACCESS_KEY and S3_SECRET_KEY are required")
	}
	if c.S3PublicEndpoint == "" {
		c.S3PublicEndpoint = c.S3Endpoint
	}
	for name, endpoint := range map[string]string{"S3_ENDPOINT": c.S3Endpoint, "S3_PUBLIC_ENDPOINT": c.S3PublicEndpoint} {
		if endpoint == "" {
			continue
		}
		u, parseErr := url.Parse(endpoint)
		if parseErr != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
			return c, fmt.Errorf("%s must be an absolute http(s) URL", name)
		}
	}
	return c, nil
}

func (c Config) DatabasePath() string { return filepath.Join(c.DataDir, "cloud.db") }

func env(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}
func boolEnv(name string, fallback bool) (bool, error) {
	v := os.Getenv(name)
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	return b, nil
}
func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	v := os.Getenv(name)
	if v == "" {
		return fallback, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return d, nil
}
func int64Env(name string, fallback int64) (int64, error) {
	v := os.Getenv(name)
	if v == "" {
		return fallback, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return n, nil
}
