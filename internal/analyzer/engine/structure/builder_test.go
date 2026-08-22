package structure

import (
	"strings"
	"testing"

	"pdfnest-backend/internal/analyzer/engine/inventory"
)

func TestBuildProjectStructure_EmptyInventory(t *testing.T) {
	inv := &inventory.ScopeInventory{
		Files: []inventory.FileEntry{},
	}

	ps, tree, err := BuildProjectStructure(inv, "empty-repo", DefaultDisplayOptions())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if ps.TotalFiles != 0 || ps.TotalDirs != 0 {
		t.Errorf("expected 0 files/dirs, got %d files, %d dirs", ps.TotalFiles, ps.TotalDirs)
	}
	if tree != "empty-repo\n" {
		t.Errorf("expected empty tree string, got %q", tree)
	}
}

func TestBuildProjectStructure_SingleFile(t *testing.T) {
	inv := &inventory.ScopeInventory{
		Files: []inventory.FileEntry{
			{RelPath: "README.md", Size: 100, IsExcluded: false, Category: inventory.CategoryDocumentation},
		},
	}

	ps, tree, err := BuildProjectStructure(inv, "repo", DefaultDisplayOptions())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if ps.TotalFiles != 1 || ps.TotalDirs != 0 {
		t.Errorf("expected 1 file 0 dirs, got %d files %d dirs", ps.TotalFiles, ps.TotalDirs)
	}

	expectedTree := "repo\n└── README.md\n"
	if tree != expectedTree {
		t.Errorf("expected:\n%s\ngot:\n%s", expectedTree, tree)
	}
}

func TestBuildProjectStructure_DeterministicOrderingAndNesting(t *testing.T) {
	// Scrambled input
	inv := &inventory.ScopeInventory{
		Files: []inventory.FileEntry{
			{RelPath: "src/utils/math.go", IsExcluded: false},
			{RelPath: "src/api/v1/controller.go", IsExcluded: false},
			{RelPath: "README.md", IsExcluded: false},
			{RelPath: "src/api/server.go", IsExcluded: false},
			{RelPath: ".env", IsExcluded: true}, // should be ignored
			{RelPath: "Makefile", IsExcluded: false},
		},
	}

	ps, tree, err := BuildProjectStructure(inv, "my-app", DefaultDisplayOptions())
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if ps.TotalFiles != 5 {
		t.Errorf("expected 5 files, got %d", ps.TotalFiles)
	}

	// Output should be deterministically sorted (directories first, then files)
	// my-app
	// ├── src
	// │   ├── api
	// │   │   ├── v1
	// │   │   │   └── controller.go
	// │   │   └── server.go
	// │   └── utils
	// │       └── math.go
	// ├── Makefile
	// └── README.md
	expectedTree := `my-app
├── src
│   ├── api
│   │   ├── v1
│   │   │   └── controller.go
│   │   └── server.go
│   └── utils
│       └── math.go
├── Makefile
└── README.md
`
	if tree != expectedTree {
		t.Errorf("expected:\n%s\ngot:\n%s", expectedTree, tree)
	}
}

func TestBuildProjectStructure_MaxNodes(t *testing.T) {
	files := []inventory.FileEntry{}
	// Add 10 files
	for i := 0; i < 10; i++ {
		files = append(files, inventory.FileEntry{
			RelPath: string(rune('a'+i)) + ".txt",
			IsExcluded: false,
		})
	}

	inv := &inventory.ScopeInventory{Files: files}

	opts := StructureDisplayOptions{MaxNodes: 5, MaxDepth: 10}
	ps, tree, err := BuildProjectStructure(inv, "test-nodes", opts)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}

	if ps.TotalFiles != 10 {
		t.Errorf("expected 10 files, got %d", ps.TotalFiles)
	}

	// 10 total nodes. Max is 5.
	// Rendered should be 5 + root name + omitted line.
	lines := strings.Split(tree, "\n")
	// root name + 5 node lines + omitted line + trailing empty string
	if len(lines) != 8 {
		t.Errorf("expected 8 lines, got %d: \n%s", len(lines), tree)
	}
	if !strings.Contains(tree, "... (5 additional files/directories omitted)") {
		t.Errorf("missing omission message, got:\n%s", tree)
	}
}

func TestBuildProjectStructure_DirOnly(t *testing.T) {
    inv := &inventory.ScopeInventory{
        Files: []inventory.FileEntry{
            {RelPath: "a/b/c/d/file.txt", IsExcluded: false},
        },
    }
    _, tree, _ := BuildProjectStructure(inv, "repo", DefaultDisplayOptions())
    
    expectedTree := `repo
└── a
    └── b
        └── c
            └── d
                └── file.txt
`
    if tree != expectedTree {
		t.Errorf("expected:\n%s\ngot:\n%s", expectedTree, tree)
    }
}
