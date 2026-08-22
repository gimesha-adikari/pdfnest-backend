package intelligence

import (
	"strings"

	"pdfnest-backend/internal/analyzer/engine/graph"
)

type ConfigUsage struct {
	ConfigID       string
	ConfigName     string
	IsSecret       bool
	IsOptional     bool
	InDocs         bool
	UsedInCode     bool
	UsageLocations []string
}

type RuntimeDeploymentInfo struct {
	Dockerfiles   []string
	DockerCompose []string
	CIWorkflows   []string
	StartupCmds   []string
	PortMappings  []string
}

type ConfigRuntimeIntelligence struct {
	ConfigUsages map[string]ConfigUsage
	Runtime      RuntimeDeploymentInfo
}

type ConfigRuntimeEngine struct {
	graph *graph.RelationshipGraph
}

func NewConfigRuntimeEngine(g *graph.RelationshipGraph) *ConfigRuntimeEngine {
	return &ConfigRuntimeEngine{graph: g}
}

func (e *ConfigRuntimeEngine) Analyze() ConfigRuntimeIntelligence {
	result := ConfigRuntimeIntelligence{
		ConfigUsages: make(map[string]ConfigUsage),
	}

	for id, entity := range e.graph.Entities {
		if entity.Kind == graph.EntityConfig {
			lowerName := strings.ToLower(entity.Name)
			usage := ConfigUsage{
				ConfigID:   id,
				ConfigName: entity.Name,
				IsSecret:   strings.Contains(lowerName, "secret") || strings.Contains(lowerName, "password") || strings.Contains(lowerName, "key") || strings.Contains(lowerName, "token"),
			}

			if props := entity.Properties; props != nil {
				if opt, ok := props["optional"].(bool); ok {
					usage.IsOptional = opt
				}
				if doc, ok := props["inDocs"].(bool); ok {
					usage.InDocs = doc
				}
			}

			if e.graph.Inbound != nil {
				for _, edge := range e.graph.Inbound[id] {
					if edge.Type == graph.RelConsumes || edge.Type == graph.RelDependsOn {
						usage.UsedInCode = true
						if source, ok := e.graph.Entities[edge.SourceID]; ok {
							usage.UsageLocations = append(usage.UsageLocations, source.Path)
						}
					}
				}
			}
			result.ConfigUsages[id] = usage
		} else if entity.Kind == graph.EntityDeployment {
			lowerPath := strings.ToLower(entity.Path)
			if strings.Contains(lowerPath, "dockerfile") {
				result.Runtime.Dockerfiles = append(result.Runtime.Dockerfiles, entity.Path)
				if props := entity.Properties; props != nil {
					if ports, ok := props["ports"].(string); ok {
						result.Runtime.PortMappings = append(result.Runtime.PortMappings, ports)
					}
					if entrypoint, ok := props["entrypoint"].(string); ok {
						result.Runtime.StartupCmds = append(result.Runtime.StartupCmds, entrypoint)
					}
				}
			} else if strings.Contains(lowerPath, "docker-compose") {
				result.Runtime.DockerCompose = append(result.Runtime.DockerCompose, entity.Path)
			} else if strings.Contains(lowerPath, ".github/workflows") || strings.Contains(lowerPath, "gitlab-ci") {
				result.Runtime.CIWorkflows = append(result.Runtime.CIWorkflows, entity.Path)
			}
		}
	}

	return result
}
