package graph

// ExplainableCycle hop represents one step in a cycle.
type CycleHop struct {
	NodeID string
	Edge   *GraphEdge
}

// ExplainableCycle represents a detected cycle with exact traversal hops and evidence.
type ExplainableCycle struct {
	Hops []CycleHop
}

// FindExplainableCycles uses DFS to detect back-edges and find explainable cycles.
func (g *RelationshipGraph) FindExplainableCycles() []ExplainableCycle {
	g.mu.RLock()
	defer g.mu.RUnlock()

	var cycles []ExplainableCycle

	visited := make(map[string]bool)
	recStack := make(map[string]bool)

	// Keep track of the path taken
	var path []CycleHop

	var dfs func(nodeID string)
	dfs = func(nodeID string) {
		visited[nodeID] = true
		recStack[nodeID] = true

		for _, edge := range g.Outbound[nodeID] {
			targetID := edge.TargetID
			
			hop := CycleHop{
				NodeID: nodeID,
				Edge:   edge,
			}
			path = append(path, hop)

			if !visited[targetID] {
				dfs(targetID)
			} else if recStack[targetID] {
				// Cycle detected
				// We need to extract the cycle from the path
				cycleStartIdx := -1
				for i := len(path) - 1; i >= 0; i-- {
					if path[i].NodeID == targetID {
						cycleStartIdx = i
						break
					}
				}

				if cycleStartIdx != -1 {
					var cycleHops []CycleHop
					// Copy the cycle from path to cycleHops
					cycleHops = append(cycleHops, path[cycleStartIdx:]...)
					cycles = append(cycles, ExplainableCycle{Hops: cycleHops})
				}
			}
			
			// Backtrack
			path = path[:len(path)-1]
		}
		recStack[nodeID] = false
	}

	for nodeID := range g.Entities {
		if !visited[nodeID] {
			dfs(nodeID)
		}
	}

	return cycles
}
