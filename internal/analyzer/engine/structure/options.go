package structure

// StructureDisplayOptions configures the bounds for the ASCII structure tree.
type StructureDisplayOptions struct {
	MaxNodes int // Max number of rendered nodes (0 for unlimited)
	MaxDepth int // Max depth to render (0 for unlimited)
}

// DefaultDisplayOptions provides standard limits to prevent unbounded output.
func DefaultDisplayOptions() StructureDisplayOptions {
	return StructureDisplayOptions{MaxNodes: 500, MaxDepth: 10}
}
