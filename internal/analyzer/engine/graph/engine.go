package graph

import (
	"fmt"
	"sort"
	"sync"
)

// RelationshipGraph represents the in-memory graph index engine.
type RelationshipGraph struct {
	mu sync.RWMutex

	Entities map[string]*GraphEntity
	Edges    map[string]*GraphEdge

	// Adjacency indexes for O(1) lookups
	Outbound map[string][]*GraphEdge
	Inbound  map[string][]*GraphEdge
}

// NewRelationshipGraph creates a new empty graph.
func NewRelationshipGraph() *RelationshipGraph {
	return &RelationshipGraph{
		Entities: make(map[string]*GraphEntity),
		Edges:    make(map[string]*GraphEdge),
		Outbound: make(map[string][]*GraphEdge),
		Inbound:  make(map[string][]*GraphEdge),
	}
}

// AddEntity adds an entity to the graph.
func (g *RelationshipGraph) AddEntity(entity *GraphEntity) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if entity.ID == "" {
		return fmt.Errorf("entity ID cannot be empty")
	}

	g.Entities[entity.ID] = entity
	return nil
}

// AddEdge adds an edge to the graph, ensuring referential integrity.
func (g *RelationshipGraph) AddEdge(edge *GraphEdge) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if edge.ID == "" {
		return fmt.Errorf("edge ID cannot be empty")
	}

	if _, exists := g.Entities[edge.SourceID]; !exists {
		return fmt.Errorf("referential integrity error: source node %q missing", edge.SourceID)
	}

	if _, exists := g.Entities[edge.TargetID]; !exists {
		return fmt.Errorf("referential integrity error: target node %q missing", edge.TargetID)
	}

	g.Edges[edge.ID] = edge
	g.Outbound[edge.SourceID] = append(g.Outbound[edge.SourceID], edge)
	g.Inbound[edge.TargetID] = append(g.Inbound[edge.TargetID], edge)

	return nil
}



// Serialize exports the graph into a deterministic JSON-serializable structure.
func (g *RelationshipGraph) Serialize() *SerializedGraph {
	g.mu.RLock()
	defer g.mu.RUnlock()

	entities := make([]GraphEntity, 0, len(g.Entities))
	for _, e := range g.Entities {
		entities = append(entities, *e)
	}
	sort.Slice(entities, func(i, j int) bool {
		return entities[i].ID < entities[j].ID
	})

	edges := make([]GraphEdge, 0, len(g.Edges))
	for _, e := range g.Edges {
		edges = append(edges, *e)
	}
	sort.Slice(edges, func(i, j int) bool {
		return edges[i].ID < edges[j].ID
	})

	return &SerializedGraph{
		Entities: entities,
		Edges:    edges,
	}
}
