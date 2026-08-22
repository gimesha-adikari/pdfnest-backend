package graph

import (
	"testing"
)

func TestIDs(t *testing.T) {
	tests := []struct {
		name     string
		got      string
		expected string
	}{
		{
			name:     "FileEntityID",
			got:      FileEntityID("./src/api/routes.go"),
			expected: "file:src/api/routes.go",
		},
		{
			name:     "SymbolEntityID",
			got:      SymbolEntityID("/src/api/routes.go", "CreateSession"),
			expected: "symbol:src/api/routes.go#CreateSession",
		},
		{
			name:     "PackageEntityID",
			got:      PackageEntityID("go", "github.com/gofiber/fiber/v2"),
			expected: "package:go:github.com/gofiber/fiber/v2",
		},
		{
			name:     "RouteEntityID",
			got:      RouteEntityID("POST", "/api/v1/analyzer/upload"),
			expected: "route:POST:/api/v1/analyzer/upload",
		},
		{
			name:     "ModelEntityID",
			got:      ModelEntityID("internal/models/session.go", "Session"),
			expected: "model:internal/models/session.go#Session",
		},
		{
			name:     "ConfigEntityID",
			got:      ConfigEntityID("REDIS_URL"),
			expected: "config:REDIS_URL",
		},
		{
			name:     "TestEntityID",
			got:      TestEntityID("./internal/api/routes_test.go"),
			expected: "test:internal/api/routes_test.go",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.expected {
				t.Errorf("expected %q, got %q", tt.expected, tt.got)
			}
		})
	}
}
