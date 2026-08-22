package intelligence

import (
	"pdfnest-backend/internal/analyzer/engine/graph"
)

type UntestedComponent struct {
	EntityID string           `json:"entityId"`
	Name     string           `json:"name"`
	Kind     graph.EntityKind `json:"kind"`
	FanIn    int              `json:"fanIn"`
}

type TestMapping struct {
	EntityID   string   `json:"entityId"`
	EntityName string   `json:"entityName"`
	TestFiles  []string `json:"testFiles"`
}

type TestIntelligence struct {
	Mappings           []TestMapping       `json:"mappings"`
	UntestedComponents []UntestedComponent `json:"untestedComponents"`
}

type TestIntelligenceEngine struct {
	graph         *graph.RelationshipGraph
	hotspotEngine *HotspotEngine
}

func NewTestIntelligenceEngine(g *graph.RelationshipGraph) *TestIntelligenceEngine {
	return &TestIntelligenceEngine{
		graph:         g,
		hotspotEngine: NewHotspotEngine(g),
	}
}

func (e *TestIntelligenceEngine) Analyze(fanInThreshold int) TestIntelligence {
	result := TestIntelligence{
		Mappings:           make([]TestMapping, 0),
		UntestedComponents: make([]UntestedComponent, 0),
	}

	mappings := make(map[string]*TestMapping)

	for id, entity := range e.graph.Entities {
		if entity.Kind == graph.EntityTest {
			continue
		}

		mapping := &TestMapping{
			EntityID:   id,
			EntityName: entity.Name,
			TestFiles:  []string{},
		}

		if e.graph.Inbound != nil {
			for _, edge := range e.graph.Inbound[id] {
				if edge.Type == graph.RelTests {
					sourceID := edge.SourceID
					if sourceEntity, ok := e.graph.Entities[sourceID]; ok {
						mapping.TestFiles = append(mapping.TestFiles, sourceEntity.Path)
					}
				}
			}
		}

		mappings[id] = mapping
	}

	for _, m := range mappings {
		if len(m.TestFiles) > 0 {
			result.Mappings = append(result.Mappings, *m)
		}
	}

	hotspots := e.hotspotEngine.Analyze()
	for _, h := range hotspots {
		if !h.IsTested && h.FanIn > fanInThreshold {
			if entity, ok := e.graph.Entities[h.EntityID]; ok {
				if entity.Kind != graph.EntityTest && entity.Kind != graph.EntityDirectory {
					result.UntestedComponents = append(result.UntestedComponents, UntestedComponent{
						EntityID: h.EntityID,
						Name:     entity.Name,
						Kind:     entity.Kind,
						FanIn:    h.FanIn,
					})
				}
			}
		}
	}

	return result
}
