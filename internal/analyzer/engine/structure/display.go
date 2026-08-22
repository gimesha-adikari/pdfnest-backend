package structure

import (
	"fmt"
	"strings"

	"pdfnest-backend/internal/analyzer/engine"
)

// GenerateASCIITree derives a human-readable string representation of the tree.
func GenerateASCIITree(ps *engine.ProjectStructure, opts StructureDisplayOptions) string {
	if ps == nil || ps.Root == nil {
		return ""
	}

	var sb strings.Builder
	sb.WriteString(ps.RootName)
	sb.WriteString("\n")

	var nodesRendered int
	totalNodes := ps.TotalFiles + ps.TotalDirs
	var limitReached bool

	var walk func(node *engine.StructureNode, prefix string, depth int)
	walk = func(node *engine.StructureNode, prefix string, depth int) {
		if limitReached {
			return
		}
		if opts.MaxDepth > 0 && depth > opts.MaxDepth {
			return
		}

		for i, child := range node.Children {
			if limitReached {
				return
			}
			if opts.MaxNodes > 0 && nodesRendered >= opts.MaxNodes {
				limitReached = true
				return
			}

			isLast := i == len(node.Children)-1
			
			var connector string
			if isLast {
				connector = "└── "
			} else {
				connector = "├── "
			}
			
			sb.WriteString(fmt.Sprintf("%s%s%s\n", prefix, connector, child.Name))
			nodesRendered++

			if opts.MaxNodes > 0 && nodesRendered >= opts.MaxNodes {
				limitReached = true
				return
			}

			if len(child.Children) > 0 {
				newPrefix := prefix
				if isLast {
					newPrefix += "    "
				} else {
					newPrefix += "│   "
				}
				walk(child, newPrefix, depth+1)
			}
		}
	}

	walk(ps.Root, "", 1)

	if totalNodes > nodesRendered {
		omitted := totalNodes - nodesRendered
		sb.WriteString(fmt.Sprintf("... (%d additional files/directories omitted)\n", omitted))
	}

	return sb.String()
}
