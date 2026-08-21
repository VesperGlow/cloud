package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("S3_BUCKET", "bucket")
	t.Setenv("S3_ACCESS_KEY", "key")
	t.Setenv("S3_SECRET_KEY", "secret")
	t.Setenv("S3_ENDPOINT", "")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr != ":8080" || c.DataDir != "/data" || c.BaseURL != "http://localhost:8080" {
		t.Fatalf("defaults: %+v", c)
	}
	if c.S3Region != "us-east-1" || c.S3PathStyle || c.PresignExpires != 15*time.Minute || c.UploadExpires != 24*time.Hour || c.GCInterval != time.Hour || c.BlockMinSize != 1<<20 || c.BlockSize != 4<<20 || c.BlockMaxSize != 16<<20 {
		t.Fatalf("defaults: %+v", c)
	}
	if c.FFmpegPath != "ffmpeg" {
		t.Fatalf("ffmpeg default=%q", c.FFmpegPath)
	}
	if c.CookieSecure {
		t.Fatal("cookie must default to insecure for http base URL")
	}
	if c.S3PublicEndpoint != "" {
		t.Fatalf("public endpoint must default to empty: %q", c.S3PublicEndpoint)
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("S3_BUCKET", "bucket")
	t.Setenv("S3_ACCESS_KEY", "key")
	t.Setenv("S3_SECRET_KEY", "secret")
	t.Setenv("APP_BASE_URL", "https://drive.example.com/")
	t.Setenv("BLOCK_SIZE", "8388608")
	t.Setenv("FASTCDC_MIN_SIZE", "2097152")
	t.Setenv("FASTCDC_MAX_SIZE", "33554432")
	t.Setenv("S3_ENDPOINT", "https://minio.example.com/")
	t.Setenv("S3_PATH_STYLE", "true")
	t.Setenv("COOKIE_SECURE", "true")
	t.Setenv("FFMPEG_PATH", "/usr/bin/ffmpeg")
	t.Setenv("S3_PUBLIC_ENDPOINT", "https://minio-public.example.com")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.BaseURL != "https://drive.example.com" {
		t.Fatalf("base url must be trimmed: %q", c.BaseURL)
	}
	if !c.CookieSecure || !c.S3PathStyle || c.BlockMinSize != 2<<20 || c.BlockSize != 8<<20 || c.BlockMaxSize != 32<<20 || c.FFmpegPath != "/usr/bin/ffmpeg" {
		t.Fatalf("overrides: %+v", c)
	}
	if c.S3PublicEndpoint != "https://minio-public.example.com" {
		t.Fatalf("public endpoint override: %q", c.S3PublicEndpoint)
	}
	// 未显式配置时公网 endpoint 回退到 S3_ENDPOINT
	t.Setenv("S3_PUBLIC_ENDPOINT", "")
	c2, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c2.S3PublicEndpoint != "https://minio.example.com" {
		t.Fatalf("public endpoint fallback: %q", c2.S3PublicEndpoint)
	}
}

func TestLoadValidations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func()
	}{
		{"missing s3 credentials", func() { t.Setenv("S3_ACCESS_KEY", "") }},
		{"bad block size", func() { t.Setenv("BLOCK_SIZE", "1024") }},
		{"bad fastcdc min", func() { t.Setenv("FASTCDC_MIN_SIZE", "1024") }},
		{"bad fastcdc max", func() { t.Setenv("FASTCDC_MAX_SIZE", "1024") }},
		{"bad base url", func() { t.Setenv("APP_BASE_URL", "not-a-url") }},
		{"bad upload expires", func() { t.Setenv("UPLOAD_EXPIRES", "0s") }},
		{"bad gc interval", func() { t.Setenv("GC_INTERVAL", "-1s") }},
		{"bad bool", func() { t.Setenv("S3_PATH_STYLE", "maybe") }},
		{"bad duration", func() { t.Setenv("PRESIGN_EXPIRES", "soon") }},
		{"bad endpoint", func() { t.Setenv("S3_ENDPOINT", "minio://host") }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("S3_BUCKET", "bucket")
			t.Setenv("S3_ACCESS_KEY", "key")
			t.Setenv("S3_SECRET_KEY", "secret")
			t.Setenv("APP_BASE_URL", "http://localhost:8080")
			t.Setenv("BLOCK_SIZE", "4194304")
			t.Setenv("FASTCDC_MIN_SIZE", "1048576")
			t.Setenv("FASTCDC_MAX_SIZE", "16777216")
			t.Setenv("UPLOAD_EXPIRES", "24h")
			t.Setenv("GC_INTERVAL", "1h")
			t.Setenv("S3_PATH_STYLE", "false")
			t.Setenv("PRESIGN_EXPIRES", "15m")
			t.Setenv("S3_ENDPOINT", "http://minio:9000")
			tc.mutate()
			if _, err := Load(); err == nil {
				t.Fatal("Load must fail")
			}
		})
	}
	// 上界内的 BLOCK_SIZE 合法（重新提供凭据，清掉上面 subtest 的污染）
	t.Setenv("S3_BUCKET", "bucket")
	t.Setenv("S3_ACCESS_KEY", "key")
	t.Setenv("S3_SECRET_KEY", "secret")
	t.Setenv("BLOCK_SIZE", "1073741824")
	t.Setenv("FASTCDC_MIN_SIZE", "268435456")
	t.Setenv("FASTCDC_MAX_SIZE", "1073741824")
	t.Setenv("S3_ENDPOINT", "")
	if _, err := Load(); err != nil {
		t.Fatalf("max block size rejected: %v", err)
	}
}

func TestDatabasePath(t *testing.T) {
	c := Config{DataDir: t.TempDir()}
	want := filepath.Join(c.DataDir, "cloud.db")
	if got := c.DatabasePath(); got != want {
		t.Fatalf("database path=%q, want %q", got, want)
	}
}
