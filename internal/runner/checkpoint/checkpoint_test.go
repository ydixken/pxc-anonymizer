package checkpoint

import (
	"reflect"
	"testing"
	"time"
)

func TestCursorPreservesTypes(t *testing.T) {
	key := []any{int64(9007199254740993), uint64(18446744073709551615), []byte{0, 255},
		"key", 1.25, true, time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC)}
	encoded, err := encode(key)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(key, decoded) {
		t.Fatalf("cursor types changed: %#v", decoded)
	}
}

func TestCursorRejectsUnsupportedEncoding(t *testing.T) {
	if _, err := encode([]any{nil}); err == nil {
		t.Fatal("nullable key accepted")
	}
	for _, data := range []string{`[{"type":"unknown","data":"x"}]`, `[{"type":"int64","data":"bad"}]`, `{`} {
		if _, err := decode([]byte(data)); err == nil {
			t.Fatal("invalid cursor accepted")
		}
	}
}
