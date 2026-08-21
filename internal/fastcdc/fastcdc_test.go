package fastcdc

import (
	"bytes"
	"io"
	"math/rand"
	"testing"
)

func TestChunkerBoundsAndRoundTrip(t *testing.T) {
	cfg, err := NewConfig(256, 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	source := make([]byte, 2<<20)
	if _, err := rand.New(rand.NewSource(42)).Read(source); err != nil {
		t.Fatal(err)
	}
	chunker := New(bytes.NewReader(source), cfg)
	var rebuilt []byte
	var sizes []int
	for {
		chunk, err := chunker.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(chunk) > cfg.Max {
			t.Fatalf("chunk exceeds max: %d", len(chunk))
		}
		if len(rebuilt)+len(chunk) < len(source) && len(chunk) < cfg.Min {
			t.Fatalf("non-final chunk below min: %d", len(chunk))
		}
		sizes = append(sizes, len(chunk))
		rebuilt = append(rebuilt, chunk...)
	}
	if !bytes.Equal(rebuilt, source) {
		t.Fatal("chunking did not round-trip the source")
	}
	if len(sizes) < 100 {
		t.Fatalf("unexpectedly few chunks: %d", len(sizes))
	}
}

func TestInsertionPreservesMostFollowingChunks(t *testing.T) {
	cfg, err := NewConfig(256, 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	base := make([]byte, 1<<20)
	if _, err := rand.New(rand.NewSource(7)).Read(base); err != nil {
		t.Fatal(err)
	}
	modified := append(append(append([]byte(nil), base[:4096]...), bytes.Repeat([]byte("insert"), 37)...), base[4096:]...)

	baseChunks := chunkBytes(t, base, cfg)
	modifiedChunks := chunkBytes(t, modified, cfg)
	seen := make(map[string]struct{}, len(baseChunks))
	for _, chunk := range baseChunks {
		seen[string(chunk)] = struct{}{}
	}
	shared := 0
	for _, chunk := range modifiedChunks {
		if _, ok := seen[string(chunk)]; ok {
			shared++
		}
	}
	if shared < len(baseChunks)*9/10 {
		t.Fatalf("only %d/%d original chunks survived an insertion", shared, len(baseChunks))
	}
}

// This vector is also produced by web/src/fastcdc.ts. It catches accidental
// drift in the Gear generator, unsigned arithmetic, masks, or normalization
// center between Go and the browser implementation.
func TestBrowserParityGoldenVector(t *testing.T) {
	cfg, err := NewConfig(256, 1024, 4096)
	if err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 1<<20)
	x := uint32(0x12345678)
	for i := range data {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		data[i] = byte(x)
	}
	chunks := chunkBytes(t, data, cfg)
	want := []int{1998, 1505, 1482, 667, 1120, 1032, 1495, 848, 672, 1228, 754, 847, 865, 1583, 796, 1539, 890, 1204, 376, 813, 983, 441, 1368, 1493}
	if len(chunks) != 1050 {
		t.Fatalf("chunk count=%d, want 1050", len(chunks))
	}
	for i, size := range want {
		if len(chunks[i]) != size {
			t.Fatalf("chunk %d size=%d, want %d", i, len(chunks[i]), size)
		}
	}
}

func chunkBytes(t *testing.T, source []byte, cfg Config) [][]byte {
	t.Helper()
	chunker := New(bytes.NewReader(source), cfg)
	var out [][]byte
	for {
		chunk, err := chunker.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, chunk)
	}
}
