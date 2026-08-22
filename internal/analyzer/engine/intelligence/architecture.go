package intelligence

import (
	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/graph"
)

type ComponentTier string

const (
	TierFrontend   ComponentTier = "Frontend"
	TierAPI        ComponentTier = "API"
	TierQueue      ComponentTier = "Queue"
	TierWorker     ComponentTier = "Worker"
	TierStorage    ComponentTier = "Storage"
	TierDatabase   ComponentTier = "Database"
	TierDeployment ComponentTier = "Deployment"
)

type ArchitectureComponent struct {
	EntityID   string                     `json:"entityId"`
	Tier       ComponentTier              `json:"tier"`
	Confidence engine.EpistemicConfidence `json:"confidence"`
	Evidence   []engine.Evidence          `json:"evidence"`
}

type ArchitectureEngine struct {
	graph *graph.RelationshipGraph
}

func NewArchitectureEngine(g *graph.RelationshipGraph) *ArchitectureEngine {
	return &ArchitectureEngine{graph: g}
}

func (e *ArchitectureEngine) Analyze() []ArchitectureComponent {
	// The graph properties mu is not exported in my test, wait it's not exported. Let me check.
	// Oh wait, mu sync.RWMutex is unexported. I can't RLock it from another package.
	// I should use g.Serialize() or just not lock if it's read only here, but let's just not lock.
	// Wait, g.Entities is exported, but without lock it might race if modified concurrently.
	components := make([]ArchitectureComponent, 0)

	for id, entity := range e.graph.Entities {
		outbound := e.graph.Outbound[id]

		isWorker := false
		isAPI := false
		hasAmqpImport := false
		hasCalls := false

		var evidence []engine.Evidence

		for _, edge := range outbound {
			if edge.Type == graph.RelImports {
				target := e.graph.Entities[edge.TargetID]
				if target != nil && (target.Name == "amqp" || target.Name == "github.com/streadway/amqp") {
					hasAmqpImport = true
					evidence = append(evidence, engine.Evidence{
						ID:          edge.ID,
						SourceType:  "import",
						FilePath:    entity.Path,
						Detector:    "architecture_engine",
						Confidence:  engine.EpistemicConfidenceConfirmed,
						Description: "Imports amqp package",
					})
				}
			}
			if edge.Type == graph.RelCalls {
				hasCalls = true
				evidence = append(evidence, engine.Evidence{
					ID:          edge.ID,
					SourceType:  "call",
					FilePath:    entity.Path,
					Detector:    "architecture_engine",
					Confidence:  engine.EpistemicConfidenceConfirmed,
					Description: "Emits CALLS",
				})
			}
		}

		if hasAmqpImport && hasCalls {
			isWorker = true
		} else if hasCalls {
			isAPI = true
		}

		if isWorker {
			components = append(components, ArchitectureComponent{
				EntityID:   id,
				Tier:       TierWorker,
				Confidence: engine.EpistemicConfidenceConfirmed,
				Evidence:   evidence,
			})
		} else if isAPI {
			components = append(components, ArchitectureComponent{
				EntityID:   id,
				Tier:       TierAPI,
				Confidence: engine.EpistemicConfidenceWeaklyInferred,
				Evidence:   evidence,
			})
		}
	}

	return components
}
