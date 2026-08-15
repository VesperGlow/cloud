package storage

import (
	"context"
	"fmt"
	"io"
)

// fileReader streams a logical file back from its blocks. It implements
// io.ReadSeeker so http.ServeContent can serve Range requests for video
// seeking; only the blocks intersecting the requested range are fetched.
type fileReader struct {
	s         *S3
	ctx       context.Context
	m         Manifest
	off       int64
	scanIdx   int
	scanStart int64
	loadedIdx int
	loaded    []byte
	err       error
}

func (s *S3) Open(ctx context.Context, key string) (io.ReadSeekCloser, error) {
	m, err := s.GetManifest(ctx, key)
	if err != nil {
		return nil, err
	}
	return &fileReader{s: s, ctx: ctx, m: m, loadedIdx: -1}, nil
}

func (r *fileReader) Read(p []byte) (int, error) {
	if r.err != nil {
		return 0, r.err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.off >= r.m.Size {
		return 0, io.EOF
	}
	idx, start, _, err := r.blockAt(r.off)
	if err != nil {
		return 0, err
	}
	data, err := r.load(idx)
	if err != nil {
		r.err = err
		return 0, err
	}
	n := copy(p, data[r.off-start:])
	r.off += int64(n)
	return n, nil
}

func (r *fileReader) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = r.off
	case io.SeekEnd:
		base = r.m.Size
	default:
		return 0, fmt.Errorf("invalid whence %d", whence)
	}
	next := base + offset
	if next < 0 {
		return 0, fmt.Errorf("negative seek position %d", next)
	}
	r.off = next
	return r.off, nil
}

func (r *fileReader) Close() error { return nil }

// blockAt locates the block containing off, advancing a cursor so that
// sequential reads cost O(1) per block and a backward seek restarts the
// scan from the beginning.
func (r *fileReader) blockAt(off int64) (int, int64, int64, error) {
	if off < r.scanStart {
		r.scanIdx, r.scanStart = 0, 0
	}
	for r.scanIdx < len(r.m.Blocks) {
		size := r.m.Blocks[r.scanIdx].Size
		if off < r.scanStart+size {
			return r.scanIdx, r.scanStart, size, nil
		}
		r.scanStart += size
		r.scanIdx++
	}
	return 0, 0, 0, io.EOF
}

func (r *fileReader) load(idx int) ([]byte, error) {
	if r.loadedIdx == idx && r.loaded != nil {
		return r.loaded, nil
	}
	block := r.m.Blocks[idx]
	data, err := r.s.GetBlock(r.ctx, block.ID)
	if err != nil {
		return nil, fmt.Errorf("read block %s: %w", block.ID, err)
	}
	if int64(len(data)) != block.Size {
		return nil, fmt.Errorf("block %s size mismatch: stored %d bytes, manifest says %d", block.ID, len(data), block.Size)
	}
	r.loaded, r.loadedIdx = data, idx
	return data, nil
}

// ReadFile reads an entire logical file with a size guard.
func (s *S3) ReadFile(ctx context.Context, key string, limit int64) ([]byte, error) {
	rc, err := s.Open(ctx, key)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrObjectTooLarge
	}
	return data, nil
}
