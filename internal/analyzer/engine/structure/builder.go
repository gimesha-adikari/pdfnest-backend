package structure

import (
	"errors"
	"sort"
	"strings"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/inventory"
)

// BuildProjectStructure builds a complete ProjectStructure from an inventory.ScopeInventory
func BuildProjectStructure(inv *inventory.ScopeInventory, rootName string, displayOpts StructureDisplayOptions) (*engine.ProjectStructure, string, error) {
	if inv == nil {
		return nil, "", errors.New("inventory is nil")
	}

	var includedFiles []inventory.FileEntry
	for _, f := range inv.Files {
		if !f.IsExcluded {
			includedFiles = append(includedFiles, f)
		}
	}

	if len(includedFiles) == 0 {
		emptyStruct := &engine.ProjectStructure{
			RootName: rootName,
			Root: &engine.StructureNode{
				Path: ".",
				Name: rootName,
				Type: engine.StructureNodeDir,
			},
			TotalFiles: 0,
			TotalDirs:  0,
		}
		return emptyStruct, rootName + "\n", nil
	}

	root := &engine.StructureNode{
		Path: ".",
		Name: rootName,
		Type: engine.StructureNodeDir,
	}

	nodeMap := make(map[string]*engine.StructureNode)
	nodeMap["."] = root

	for _, f := range includedFiles {
		parts := strings.Split(f.RelPath, "/")
		currentPath := ""
		
		for i := 0; i < len(parts)-1; i++ {
			if currentPath == "" {
				currentPath = parts[i]
			} else {
				currentPath = currentPath + "/" + parts[i]
			}
			
			if _, exists := nodeMap[currentPath]; !exists {
				node := &engine.StructureNode{
					Path: currentPath,
					Name: parts[i],
					Type: engine.StructureNodeDir,
				}
				nodeMap[currentPath] = node
			}
		}
		
		fileNode := &engine.StructureNode{
			Path:     f.RelPath,
			Name:     parts[len(parts)-1],
			Type:     engine.StructureNodeFile,
			Size:     f.Size,
			Category: string(f.Category),
			Language: f.Language,
		}
		nodeMap[f.RelPath] = fileNode
	}

	var allPaths []string
	for p := range nodeMap {
		if p != "." {
			allPaths = append(allPaths, p)
		}
	}

	var totalFiles, totalDirs int
	for _, node := range nodeMap {
		if node.Type == engine.StructureNodeFile {
			totalFiles++
		} else if node.Path != "." {
			totalDirs++
		}
	}

	for _, p := range allPaths {
		node := nodeMap[p]
		parentPath := ""
		lastSlash := strings.LastIndex(p, "/")
		if lastSlash == -1 {
			parentPath = "."
		} else {
			parentPath = p[:lastSlash]
		}
		
		parentNode := nodeMap[parentPath]
		if parentNode != nil {
			parentNode.Children = append(parentNode.Children, node)
		}
	}

	sortTree(root)

	ps := &engine.ProjectStructure{
		RootName:   rootName,
		Root:       root,
		TotalFiles: totalFiles,
		TotalDirs:  totalDirs,
	}

	asciiTree := GenerateASCIITree(ps, displayOpts)

	return ps, asciiTree, nil
}

func sortTree(node *engine.StructureNode) {
	if node == nil || len(node.Children) == 0 {
		return
	}

	sort.Slice(node.Children, func(i, j int) bool {
		a := node.Children[i]
		b := node.Children[j]
		if a.Type != b.Type {
			return a.Type == engine.StructureNodeDir
		}
		return a.Name < b.Name
	})

	for _, child := range node.Children {
		sortTree(child)
	}
}
