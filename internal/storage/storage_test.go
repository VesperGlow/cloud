package storage

import (
	"context"
	"net/url"
	"testing"
	"time"

	"github.com/VesperGlow/cloud/internal/config"
)

func TestPresignUsesBrowserReachableEndpoint(t *testing.T) {
	store, err := NewS3(context.Background(), config.Config{
		S3Endpoint:       "http://minio:9000",
		S3PublicEndpoint: "http://localhost:9000",
		S3Region:         "us-east-1",
		S3Bucket:         "cloud",
		S3AccessKey:      "access-key",
		S3SecretKey:      "secret-key",
		S3PathStyle:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	signed, err := store.PresignPut(context.Background(), "objects/test", "application/octet-stream", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if u.Host != "localhost:9000" {
		t.Fatalf("presigned host = %q, want browser endpoint", u.Host)
	}
	if u.Path != "/cloud/objects/test" {
		t.Fatalf("presigned path = %q", u.Path)
	}
}
