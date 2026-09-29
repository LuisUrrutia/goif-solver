package oif_test

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"testing"
)

func validateSchema(t *testing.T, name string, value any) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	validateRawSchema(t, name, raw)
}

func validateRawSchema(t *testing.T, name string, raw []byte) {
	t.Helper()
	snapshot, err := os.ReadFile("testdata/schemas.json")
	if err != nil {
		t.Fatal(err)
	}
	var schemas map[string]map[string]any
	var value any
	if err = json.Unmarshal(snapshot, &schemas); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if err = matchSchema(schemas[name], value); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
}

// This validates the keywords used by the pinned OpenAPI snapshot. Unknown
// keywords fail instead of silently weakening the conformance check.
func matchSchema(schema map[string]any, value any) error {
	for key := range schema {
		switch key {
		case "type", "properties", "required", "items", "enum", "anyOf", "pattern", "additionalProperties", "nullable", "default":
		default:
			return fmt.Errorf("unhandled schema keyword %s", key)
		}
	}
	if value == nil && schema["nullable"] == true {
		return nil
	}
	if variants, ok := schema["anyOf"].([]any); ok {
		for _, variant := range variants {
			if err := matchSchema(variant.(map[string]any), value); err == nil {
				return nil
			}
		}
		return fmt.Errorf("no matching union member")
	}
	if values, ok := schema["enum"].([]any); ok {
		found := false
		for _, expected := range values {
			found = found || value == expected
		}
		if !found {
			return fmt.Errorf("value outside enum: %v", value)
		}
	}
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("expected object")
		}
		if required, ok := schema["required"].([]any); ok {
			for _, field := range required {
				if _, ok := object[field.(string)]; !ok {
					return fmt.Errorf("missing field %s", field)
				}
			}
		}
		properties, _ := schema["properties"].(map[string]any)
		for name, field := range object {
			child, ok := properties[name].(map[string]any)
			if !ok {
				child, ok = schema["additionalProperties"].(map[string]any)
			}
			if ok {
				if err := matchSchema(child, field); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
			}
		}
	case "array":
		array, ok := value.([]any)
		if !ok {
			return fmt.Errorf("expected array")
		}
		for _, item := range array {
			if err := matchSchema(schema["items"].(map[string]any), item); err != nil {
				return err
			}
		}
	case "string":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("expected string")
		}
		if pattern, ok := schema["pattern"].(string); ok {
			matched, err := regexp.MatchString(pattern, text)
			if err != nil || !matched {
				return fmt.Errorf("invalid string pattern")
			}
		}
	case "number":
		if _, ok := value.(float64); !ok {
			return fmt.Errorf("expected number")
		}
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("expected boolean")
		}
	case nil:
	default:
		return fmt.Errorf("unsupported schema type %v", schema["type"])
	}
	return nil
}
