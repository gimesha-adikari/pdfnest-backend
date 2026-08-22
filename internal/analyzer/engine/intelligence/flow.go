package intelligence

import (
	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/graph"
)

type FlowStep struct {
	EntityID   string
	Action     string // e.g., "HTTP Request", "Calls", "Persists To"
	TargetID   string
	Confidence engine.EpistemicConfidence
}

type ExecutionFlow struct {
	ID    string
	Steps []FlowStep
}

type ExecutionFlowEngine struct {
	graph *graph.RelationshipGraph
}

func NewExecutionFlowEngine(g *graph.RelationshipGraph) *ExecutionFlowEngine {
	return &ExecutionFlowEngine{graph: g}
}

func (e *ExecutionFlowEngine) Analyze() []ExecutionFlow {

	var flows []ExecutionFlow

	// Find entrypoints (e.g., routes)
	for id, entity := range e.graph.Entities {
		if entity.Kind == graph.EntityRoute {
			flow := ExecutionFlow{
				ID: "flow_" + id,
			}
			visited := make(map[string]bool)
			e.trace(id, &flow, visited)
			
			if len(flow.Steps) > 0 {
				flows = append(flows, flow)
			}
		}
	}

	return flows
}

func (e *ExecutionFlowEngine) trace(currentID string, flow *ExecutionFlow, visited map[string]bool) {
	if visited[currentID] {
		return // Cycle detection
	}
	visited[currentID] = true

	outbound := e.graph.Outbound[currentID]
	for _, edge := range outbound {
		action := ""
		conf := engine.EpistemicConfidenceWeaklyInferred

		switch edge.Type {
		case graph.RelCalls:
			action = "Calls"
			conf = engine.EpistemicConfidenceConfirmed
		case graph.RelPersistsTo:
			action = "Persists To"
			conf = engine.EpistemicConfidenceConfirmed
		case graph.RelPublishesTo:
			action = "Publishes To"
			conf = engine.EpistemicConfidenceConfirmed
		case graph.RelExposes: // If a controller exposes a route, wait... the route is the entrypoint. 
			// A file exposes a route? No, a Route might call a Controller?
			action = "Delegates To"
			conf = engine.EpistemicConfidenceConfirmed
		case graph.RelConsumes:
			action = "Consumes"
			conf = engine.EpistemicConfidenceConfirmed
		}

		if action != "" && !visited[edge.TargetID] {
			flow.Steps = append(flow.Steps, FlowStep{
				EntityID:   currentID,
				Action:     action,
				TargetID:   edge.TargetID,
				Confidence: conf,
			})
			e.trace(edge.TargetID, flow, visited)
		}
	}
}
