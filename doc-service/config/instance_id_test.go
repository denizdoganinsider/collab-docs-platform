package config

import (
	"strings"
	"testing"
)

func TestValidateInstanceID(t *testing.T) {
	accepted := []string{"doc-9001", "doc-9011", "a", "doc_eu.west-1", strings.Repeat("x", 64)}
	for _, id := range accepted {
		if err := ValidateInstanceID(id); err != nil {
			t.Errorf("ValidateInstanceID(%q) = %v, want nil", id, err)
		}
	}

	rejected := map[string]string{
		"empty":        "",
		"space":        "doc 9001",
		"slash":        "doc/9001",
		"colon":        "doc:9001",
		"newline":      "doc\n9001",
		"non-ascii":    "döc-9001",
		"too long":     strings.Repeat("x", 65),
		"header-break": "doc\r\nX-Other: 1",
	}
	for name, id := range rejected {
		if err := ValidateInstanceID(id); err == nil {
			t.Errorf("%s: ValidateInstanceID(%q) accepted", name, id)
		}
	}
}
