package intelligence

import (
	"pdfnest-backend/internal/analyzer/engine/graph"
	"sort"
)

type HotspotScore struct {
	EntityID      string  `json:"entityId"`
	FanIn         int     `json:"fanIn"`
	FanOut        int     `json:"fanOut"`
	Centrality    float64 `json:"centrality"`
	Complexity    float64 `json:"complexity"`
	IsTested      bool    `json:"isTested"`
	HotspotMetric float64 `json:"hotspotMetric"`
}

type EdgeWeights map[graph.RelationType]float64

var DefaultEdgeWeights = EdgeWeights{
	graph.RelImports:    1.0,
	graph.RelCalls:      2.0,
	graph.RelExposes:    5.0,
	graph.RelDependsOn:  3.0,
	graph.RelConsumes:   2.0,
	graph.RelPersistsTo: 4.0,
	graph.RelTests:      1.0,
}

type HotspotEngine struct {
	graph   *graph.RelationshipGraph
	weights EdgeWeights
}

func NewHotspotEngine(g *graph.RelationshipGraph) *HotspotEngine {
	return &HotspotEngine{
		graph:   g,
		weights: DefaultEdgeWeights,
	}
}

func (e *HotspotEngine) Analyze() []HotspotScore {
	scores := make([]HotspotScore, 0)

	for id, entity := range e.graph.Entities {
		fanIn := 0
		fanOut := 0
		var weightedIn float64
		var weightedOut float64
		isTested := false

		if e.graph.Inbound != nil {
			for _, edge := range e.graph.Inbound[id] {
				if edge.Type == graph.RelTests {
					isTested = true
					continue
				}
				fanIn++
				weight := 1.0
				if w, ok := e.weights[edge.Type]; ok {
					weight = w
				}
				weightedIn += weight
			}
		}

		if e.graph.Outbound != nil {
			for _, edge := range e.graph.Outbound[id] {
				fanOut++
				weight := 1.0
				if w, ok := e.weights[edge.Type]; ok {
					weight = w
				}
				weightedOut += weight
			}
		}

		centrality := weightedIn + weightedOut

		complexity := 0.0
		if entity.Properties != nil {
			if val, ok := entity.Properties["complexity"].(float64); ok {
				complexity = val
			} else if val, ok := entity.Properties["complexity"].(int); ok {
				complexity = float64(val)
			}
		}

		hotspot := centrality + (complexity * 2.0)

		// If node has high fan-in or centrality but lacks tests, penalize it to highlight as hotspot
		if !isTested && (fanIn > 0 || complexity > 5) {
			hotspot *= 1.5
		}

		scores = append(scores, HotspotScore{
			EntityID:      id,
			FanIn:         fanIn,
			FanOut:        fanOut,
			Centrality:    centrality,
			Complexity:    complexity,
			IsTested:      isTested,
			HotspotMetric: hotspot,
		})
	}

	// Sort by hotspot metric descending
	sort.Slice(scores, func(i, j int) bool {
		return scores[i].HotspotMetric > scores[j].HotspotMetric
	})

	return scores
}
