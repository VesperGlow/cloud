// Package fastcdc implements deterministic content-defined chunking for both
// server-side writes and the matching browser worker implementation.
package fastcdc

import (
	"bufio"
	"errors"
	"io"
	"math"
)

// Config controls the lower bound, target average, and hard upper bound of a
// chunk. The average does not need to be a power of two; the closest mask is
// selected in that case.
type Config struct {
	Min int
	Avg int
	Max int

	center    int
	smallMask uint32
	largeMask uint32
}

func NewConfig(minimum, average, maximum int) (Config, error) {
	if minimum < 1 || minimum > average || average > maximum {
		return Config{}, errors.New("fastcdc sizes must satisfy 1 <= min <= avg <= max")
	}
	bits := int(math.Round(math.Log2(float64(average))))
	if bits < 2 || bits > 30 {
		return Config{}, errors.New("fastcdc average size is outside the supported mask range")
	}
	// Switching before Avg compensates for the skipped [0, Min) region and
	// yields a tighter distribution around the requested target.
	center := average - minimum - (minimum+1)/2
	if center < minimum {
		center = minimum
	}
	if center > maximum {
		center = maximum
	}
	return Config{
		Min:       minimum,
		Avg:       average,
		Max:       maximum,
		center:    center,
		smallMask: mask(bits + 1),
		largeMask: mask(bits - 1),
	}, nil
}

func mask(bits int) uint32 { return uint32((uint64(1) << bits) - 1) }

// Cut returns the next content-defined boundary. It skips the minimum region,
// uses a stricter mask before the normalization center, an easier mask after
// it, and forces a cut at Max.
func (c Config) Cut(data []byte) int {
	limit := min(len(data), c.Max)
	if limit <= c.Min {
		return limit
	}
	pattern := uint32(0)
	i := c.Min
	barrier := min(c.center, limit)
	for ; i < barrier; i++ {
		pattern = (pattern >> 1) + gear[data[i]]
		if pattern&c.smallMask == 0 {
			return i + 1
		}
	}
	for ; i < limit; i++ {
		pattern = (pattern >> 1) + gear[data[i]]
		if pattern&c.largeMask == 0 {
			return i + 1
		}
	}
	return limit
}

// Chunker incrementally splits an io.Reader without loading the whole file.
// Next returns an owned byte slice that remains valid after the next call.
type Chunker struct {
	r   *bufio.Reader
	cfg Config
}

func New(r io.Reader, cfg Config) *Chunker {
	return &Chunker{r: bufio.NewReaderSize(r, cfg.Max), cfg: cfg}
}

func (c *Chunker) Next() ([]byte, error) {
	data, err := c.r.Peek(c.cfg.Max)
	if len(data) == 0 {
		if err == nil {
			return nil, io.ErrNoProgress
		}
		return nil, err
	}
	cut := c.cfg.Cut(data)
	chunk := append([]byte(nil), data[:cut]...)
	if _, discardErr := c.r.Discard(cut); discardErr != nil {
		return nil, discardErr
	}
	return chunk, nil
}

// The Gear table is generated deterministically from a fixed seed using the
// Murmur3 32-bit finalizer. Keeping the same generator in TypeScript makes
// browser and server chunk boundaries byte-for-byte identical without a large
// duplicated constant table.
var gear = makeGear()

func makeGear() [256]uint32 {
	var table [256]uint32
	for i := range table {
		x := uint32(i) + 0x9e3779b9
		x ^= x >> 16
		x *= 0x85ebca6b
		x ^= x >> 13
		x *= 0xc2b2ae35
		x ^= x >> 16
		table[i] = x & math.MaxInt32
	}
	return table
}
