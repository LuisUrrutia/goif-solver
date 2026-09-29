package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDecodePreservesJSONDiagnostics(t *testing.T) {
	for _, raw := range []string{`{"namespace":"synthetic-secret","version": !}`, `{} !`} {
		t.Run(raw, func(t *testing.T) {
			_, err := Decode[Config](json.RawMessage(raw))

			var syntax *json.SyntaxError
			if !errors.As(err, &syntax) || syntax.Offset == 0 {
				t.Fatalf("syntax cause lost: %v", err)
			}
			if !strings.Contains(err.Error(), "byte "+strconv.FormatInt(syntax.Offset, 10)) || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("missing location or exposed value: %v", err)
			}
		})
	}
	t.Run("type", func(t *testing.T) {
		_, err := Decode[Config](json.RawMessage(`{"version":"synthetic-secret"}`))

		var mismatch *json.UnmarshalTypeError
		if !errors.As(err, &mismatch) || mismatch.Field != "version" {
			t.Fatalf("type cause lost: %v", err)
		}
		if !strings.Contains(err.Error(), "version") || strings.Contains(err.Error(), "synthetic-secret") {
			t.Fatalf("missing field or exposed value: %v", err)
		}
	})
	t.Run("unknown field", func(t *testing.T) {
		_, err := Decode[Config](json.RawMessage(`{"workerz":1}`))

		if err == nil || !strings.Contains(err.Error(), `unknown field "workerz"`) {
			t.Fatalf("unknown field lost: %v", err)
		}
	})
	t.Run("trailing document", func(t *testing.T) {
		_, err := Decode[Config](json.RawMessage(`{} {}`))

		if err == nil || !strings.Contains(err.Error(), "trailing JSON data") {
			t.Fatalf("trailing document accepted: %v", err)
		}
	})
}

func TestLoadDistinguishesFileAndDecodeFailures(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		_, err := Load(filepath.Join(t.TempDir(), "missing.json"))

		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("filesystem cause lost: %v", err)
		}
	})
	t.Run("read failure", func(t *testing.T) {
		_, err := Load(t.TempDir())

		var pathError *fs.PathError
		if !errors.As(err, &pathError) || !strings.Contains(err.Error(), "read configuration") {
			t.Fatalf("read failure misclassified: %v", err)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "oversized.json")
		if err := os.WriteFile(path, []byte(strings.Repeat(" ", (1<<20)+1)), 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := Load(path)

		if err == nil || !strings.Contains(err.Error(), "configuration exceeds bounds") {
			t.Fatalf("size limit lost: %v", err)
		}
	})
	t.Run("malformed", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "invalid.json")
		if err := os.WriteFile(path, []byte(`{"version": !}`), 0o600); err != nil {
			t.Fatal(err)
		}

		_, err := Load(path)

		var syntax *json.SyntaxError
		if !errors.As(err, &syntax) {
			t.Fatalf("configuration syntax cause lost: %v", err)
		}
	})
}
