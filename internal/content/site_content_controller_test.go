package content

import (
	"testing"
)

func TestFilterAllowed(t *testing.T) {
	allowed := map[string]struct{}{
		"title":       {},
		"description": {},
	}

	input := map[string]interface{}{
		"title":       "New Title",
		"description": "New Description",
		"id":          100,
		"ID":          100,
		"createdAt":   "2026-01-01",
		"malicious":   "DROP_ME",
	}

	filtered := filterAllowed(input, allowed)

	if filtered["title"] != "New Title" {
		t.Errorf("expected title to be preserved, got %v", filtered["title"])
	}
	if filtered["description"] != "New Description" {
		t.Errorf("expected description to be preserved, got %v", filtered["description"])
	}
	if _, exists := filtered["id"]; exists {
		t.Errorf("id should have been filtered out")
	}
	if _, exists := filtered["ID"]; exists {
		t.Errorf("ID should have been filtered out")
	}
	if _, exists := filtered["createdAt"]; exists {
		t.Errorf("createdAt should have been filtered out")
	}
	if _, exists := filtered["malicious"]; exists {
		t.Errorf("malicious field should have been filtered out")
	}
	if _, exists := filtered["updatedAt"]; !exists {
		t.Errorf("updatedAt timestamp should have been added")
	}
}
