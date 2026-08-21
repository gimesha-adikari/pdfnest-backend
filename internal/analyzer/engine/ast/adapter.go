package ast

import (
	"sort"
	"strings"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/parsers"
)

// EnrichAnalysisFacts enriches preliminary Phase 3 static facts with deeper AST-derived discoveries.
func EnrichAnalysisFacts(facts *parsers.AnalysisFacts, astResult *ASTAnalysisResult) {
	if facts == nil || astResult == nil {
		return
	}

	// 1. Merge & Upgrade Routes
	routeMap := make(map[string]*engine.ApiRouteItem)
	for i := range facts.Routes {
		r := &facts.Routes[i]
		key := strings.ToUpper(r.Method) + ":" + r.Path
		routeMap[key] = r
	}

	for _, astRoute := range astResult.Routes {
		key := strings.ToUpper(astRoute.Method) + ":" + astRoute.Path
		if existing, ok := routeMap[key]; ok {
			// Upgrade static route with AST handler and exact line number
			if astRoute.InferredHandler != nil && *astRoute.InferredHandler != "" {
				existing.InferredHandler = astRoute.InferredHandler
			}
			if astRoute.LineNumber != nil && *astRoute.LineNumber > 0 {
				existing.LineNumber = astRoute.LineNumber
			}
			if astRoute.AuthRequired {
				existing.AuthRequired = true
			}
		} else {
			// Add newly discovered AST route
			facts.Routes = append(facts.Routes, astRoute)
			routeMap[key] = &astRoute
		}
	}

	// Sort routes deterministically
	sort.Slice(facts.Routes, func(i, j int) bool {
		if facts.Routes[i].Method != facts.Routes[j].Method {
			return facts.Routes[i].Method < facts.Routes[j].Method
		}
		if facts.Routes[i].Path != facts.Routes[j].Path {
			return facts.Routes[i].Path < facts.Routes[j].Path
		}
		return facts.Routes[i].SourceFile < facts.Routes[j].SourceFile
	})

	// 2. Enrich Environment Variable References
	envRefMap := make(map[string][]string)
	for _, ref := range astResult.EnvironmentReferences {
		upperName := strings.ToUpper(ref.Name)
		envRefMap[upperName] = append(envRefMap[upperName], ref.SourceFile)
	}

	for i := range facts.Environment {
		envVar := &facts.Environment[i]
		upperName := strings.ToUpper(envVar.Name)
		if refs, ok := envRefMap[upperName]; ok {
			seen := make(map[string]bool)
			for _, r := range envVar.References {
				seen[r] = true
			}
			for _, r := range refs {
				if !seen[r] {
					seen[r] = true
					envVar.References = append(envVar.References, r)
				}
			}
			sort.Strings(envVar.References)
		}
	}

	// 3. Enrich Technology Evidence
	if len(astResult.Evidence) > 0 && len(facts.Technologies) > 0 {
		for i := range facts.Technologies {
			tech := &facts.Technologies[i]
			for _, ev := range astResult.Evidence {
				if techMatchesEvidence(tech.Name, ev.Detail) {
					tech.Evidence = append(tech.Evidence, ev)
				}
			}
			// Sort evidence
			sort.Slice(tech.Evidence, func(x, y int) bool {
				if tech.Evidence[x].FilePath != tech.Evidence[y].FilePath {
					return tech.Evidence[x].FilePath < tech.Evidence[y].FilePath
				}
				return tech.Evidence[x].Detail < tech.Evidence[y].Detail
			})
		}
	}
}

func techMatchesEvidence(techName, detail string) bool {
	lowerTech := strings.ToLower(techName)
	lowerDetail := strings.ToLower(detail)
	return strings.Contains(lowerDetail, lowerTech)
}
