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
	Addr             string
	DataDir          string
	BaseURL          string
	CookieSecure     bool
	AdminUsername    string
	AdminPassword    string
	S3Endpoint       string
	S3PublicEndpoint string
	S3Region         string
	S3Bucket         string
	S3AccessKey      string
	S3SecretKey      string
	S3PathStyle      bool
	PresignExpires   time.Duration
	// BlockSize is the target average FastCDC chunk size. BlockMinSize and
	// BlockMaxSize bound the variable-size chunks around that target.
	BlockMinSize  int64
	BlockSize     int64
	BlockMaxSize  int64
	UploadExpires time.Duration
	GCInterval    time.Duration
	FFmpegPath    string
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
		FFmpegPath:       os.Getenv("FFMPEG_PATH"),
	}
	if c.FFmpegPath == "" {
		c.FFmpegPath = "ffmpeg"
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
	if c.GCInterval, err = durationEnv("GC_INTERVAL", time.Hour); err != nil {
		return c, err
	}
	if c.BlockSize, err = int64Env("BLOCK_SIZE", 4*1024*1024); err != nil {
		return c, err
	}
	if c.BlockMinSize, err = int64Env("FASTCDC_MIN_SIZE", c.BlockSize/4); err != nil {
		return c, err
	}
	defaultMax := min(c.BlockSize*4, int64(1024*1024*1024))
	if c.BlockMaxSize, err = int64Env("FASTCDC_MAX_SIZE", defaultMax); err != nil {
		return c, err
	}
	if c.BlockSize < 1*1024*1024 || c.BlockSize > 1024*1024*1024 {
		return c, errors.New("BLOCK_SIZE must be between 1 MiB and 1 GiB")
	}
	if c.BlockMinSize < 64*1024 || c.BlockMinSize > c.BlockSize {
		return c, errors.New("FASTCDC_MIN_SIZE must be between 64 KiB and BLOCK_SIZE")
	}
	if c.BlockMaxSize < c.BlockSize || c.BlockMaxSize > 1024*1024*1024 {
		return c, errors.New("FASTCDC_MAX_SIZE must be between BLOCK_SIZE and 1 GiB")
	}
	if c.UploadExpires <= 0 {
		return c, errors.New("UPLOAD_EXPIRES must be positive")
	}
	if c.GCInterval < 0 {
		return c, errors.New("GC_INTERVAL must not be negative")
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

func (c Config) DatabasePath() string { return filepath.Join(c.DataDir, "revaro.db") }

// ChunkSizes also supplies useful defaults for tests and embedded callers
// that construct Config directly instead of using Load.
func (c Config) ChunkSizes() (minimum, average, maximum int64) {
	average = c.BlockSize
	if average <= 0 {
		average = 4 * 1024 * 1024
	}
	minimum = c.BlockMinSize
	if minimum <= 0 {
		minimum = max(1, average/4)
	}
	maximum = c.BlockMaxSize
	if maximum <= 0 {
		maximum = min(average*4, int64(1024*1024*1024))
	}
	return minimum, average, maximum
}

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
