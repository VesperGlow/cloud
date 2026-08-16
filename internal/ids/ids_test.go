package ids

import (
	"regexp"
	"testing"
)

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestNewIsValidRFC4122V4(t *testing.T) {
	for i := 0; i < 100; i++ {
		id := New()
		if !uuidRe.MatchString(id) {
			t.Fatalf("id %q is not a canonical UUID", id)
		}
		// 版本位 = 4，变体位 = 8/9/a/b
		if id[14] != '4' {
			t.Fatalf("id %q version nibble = %c, want 4", id, id[14])
		}
		switch id[19] {
		case '8', '9', 'a', 'b':
		default:
			t.Fatalf("id %q variant nibble = %c", id, id[19])
		}
	}
}

func TestNewIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := New()
		if seen[id] {
			t.Fatalf("duplicate id %q", id)
		}
		seen[id] = true
	}
}
