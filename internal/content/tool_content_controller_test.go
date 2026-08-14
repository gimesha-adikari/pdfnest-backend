package content

import (
	"testing"
)

func TestNormalizeHref(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"merge-pdf", "/merge-pdf"},
		{"/merge-pdf", "/merge-pdf"},
		{"/merge-pdf/", "/merge-pdf"},
		{" /merge-pdf ", "/merge-pdf"},
		{"", ""},
		{"/", "/"},
	}

	for _, tt := range tests {
		result := normalizeHref(tt.input)
		if result != tt.expected {
			t.Errorf("normalizeHref(%q) = %q; want %q", tt.input, result, tt.expected)
		}
	}
}

func TestValidateCategory(t *testing.T) {
	validCategories := []string{"organize", "edit", "convert", "create", "security", "optimize", "studio", "ORGANIZE", ""}
	invalidCategories := []string{"invalid", "admin", "other", "random"}

	for _, cat := range validCategories {
		if !validateCategory(cat) {
			t.Errorf("validateCategory(%q) should be valid", cat)
		}
	}

	for _, cat := range invalidCategories {
		if validateCategory(cat) {
			t.Errorf("validateCategory(%q) should be invalid", cat)
		}
	}
}

func TestValidateJSONString(t *testing.T) {
	validJSONs := []string{
		"",
		"[]",
		"{}",
		`[{"question":"Q","answer":"A"}]`,
		`["keyword1","keyword2"]`,
	}
	invalidJSONs := []string{
		"{broken",
		"[invalid",
		`{"key": }`,
	}

	for _, j := range validJSONs {
		if !validateJSONString(j) {
			t.Errorf("validateJSONString(%q) should be valid", j)
		}
	}

	for _, j := range invalidJSONs {
		if validateJSONString(j) {
			t.Errorf("validateJSONString(%q) should be invalid", j)
		}
	}
}

func TestNormalizeMapKeys(t *testing.T) {
	raw := map[string]interface{}{
		"Href":         "merge-pdf",
		"Title":        "Merge PDF",
		"IsActive":     true,
		"SortOrder":    float64(5),
		"KeywordsJson": `["merge","pdf"]`,
	}

	norm := normalizeMapKeys(raw)

	if norm["href"] != "/merge-pdf" {
		t.Errorf("expected href to be /merge-pdf, got %v", norm["href"])
	}
	if norm["title"] != "Merge PDF" {
		t.Errorf("expected title to be Merge PDF, got %v", norm["title"])
	}
	if norm["isActive"] != true {
		t.Errorf("expected isActive to be true, got %v", norm["isActive"])
	}
	if norm["sortOrder"] != float64(5) {
		t.Errorf("expected sortOrder to be 5, got %v", norm["sortOrder"])
	}
	if norm["keywordsJson"] != `["merge","pdf"]` {
		t.Errorf("expected keywordsJson to be [\"merge\",\"pdf\"], got %v", norm["keywordsJson"])
	}
}
