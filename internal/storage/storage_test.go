package storage

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/VesperGlow/revaro/internal/config"
)

func TestPutBlockRejectsHashMismatch(t *testing.T) {
	store, err := NewS3(context.Background(), config.Config{
		S3Region:    "us-east-1",
		S3Bucket:    "revaro",
		S3AccessKey: "access-key",
		S3SecretKey: "secret-key",
		BlockSize:   4 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("ab", 32)
	if err := store.PutBlock(context.Background(), id, []byte("different content")); !errors.Is(err, ErrBlockHashMismatch) {
		t.Fatalf("PutBlock error=%v", err)
	}
}

func TestPresignBlockPutUsesBrowserEndpointAndConditionalHeader(t *testing.T) {
	store, err := NewS3(context.Background(), config.Config{
		S3Endpoint:       "http://minio:9000",
		S3PublicEndpoint: "http://localhost:9000",
		S3Region:         "us-east-1",
		S3Bucket:         "revaro",
		S3AccessKey:      "access-key",
		S3SecretKey:      "secret-key",
		S3PathStyle:      true,
		BlockSize:        4 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := strings.Repeat("ab", 32)
	signed, err := store.PresignBlockPut(context.Background(), id, time.Minute)
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
	want := "/revaro/blocks/" + id[:2] + "/" + id[2:]
	if u.Path != want {
		t.Fatalf("presigned path = %q, want %q", u.Path, want)
	}
	signedHeaders := u.Query().Get("X-Amz-SignedHeaders")
	for _, h := range strings.Split(signedHeaders, ";") {
		if h == "if-none-match" {
			return
		}
	}
	t.Fatalf("signed headers %q do not include if-none-match", signedHeaders)
}

func TestManifestIDAndKeyAreContentAddressed(t *testing.T) {
	block := Block{ID: strings.Repeat("01", 32), Size: 6}
	m := Manifest{Version: 1, Size: 6, Blocks: []Block{block}}
	id := m.ID()
	if !ValidBlockID(id) {
		t.Fatalf("manifest id %q is not valid lowercase hex64", id)
	}
	if m.Key() != "manifests/"+id[:2]+"/"+id[2:] {
		t.Fatalf("manifest key %q", m.Key())
	}
	same := Manifest{Version: 1, Size: 6, Blocks: []Block{block}}
	if same.ID() != id {
		t.Fatal("manifest id is not deterministic")
	}
	other := Manifest{Version: 1, Size: 7, Blocks: []Block{{ID: block.ID, Size: 7}}}
	if other.ID() == id {
		t.Fatal("different manifests share an id")
	}
}

func TestBlockKeyAndValidation(t *testing.T) {
	id := strings.Repeat("cd", 32)
	if got := BlockKey(id); got != "blocks/"+id[:2]+"/"+id[2:] {
		t.Fatalf("block key %q", got)
	}
	if !IsManifestKey("manifests/" + id[:2] + "/" + id[2:]) {
		t.Fatal("manifest key not recognized")
	}
	if IsManifestKey("blocks/" + id[:2] + "/" + id[2:]) {
		t.Fatal("block key misdetected as manifest key")
	}
	for _, bad := range []string{"", "abcd", strings.Repeat("g", 64), strings.Repeat("AB", 32), strings.Repeat("ab", 31)} {
		if ValidBlockID(bad) {
			t.Fatalf("ValidBlockID(%q) = true, want false", bad)
		}
	}
	if !ValidBlockID(id) {
		t.Fatal("ValidBlockID rejects a valid id")
	}
}
