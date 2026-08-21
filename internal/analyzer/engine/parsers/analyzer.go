package parsers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/inventory"
)

// AnalysisFacts groups all deterministic static repository facts extracted during Phase 3.
type AnalysisFacts struct {
	Manifests    []*ManifestResult
	RuntimeDeps  []engine.DependencyItem
	DevDeps      []engine.DependencyItem
	Technologies []engine.TechnologyItem
	Environment  []engine.EnvironmentVariable
	Routes       []engine.ApiRouteItem
	Testing      engine.TestingInfo
	Deployment   engine.DeploymentInfo
	Setup        engine.SetupInfo
	Warnings     []string
}

// AnalyzeRepositoryFacts orchestrates static manifest parsing, dependency merging, technology evidence detection,
// negative assertion calculation, environment scanning, route extraction, and testing/deployment fact extraction.
func AnalyzeRepositoryFacts(
	ctx context.Context,
	rootDir string,
	inv *inventory.ScopeInventory,
) (*AnalysisFacts, error) {
	if inv == nil {
		return nil, fmt.Errorf("scope inventory is nil")
	}

	registry := NewRegistry()
	manifestResults := make([]*ManifestResult, 0, len(inv.ManifestsFound))
	allWarnings := make([]string, 0)

	// 1. Parse all discovered included manifest files
	for _, file := range inv.Files {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}

		if file.IsExcluded || file.Category != inventory.CategoryManifest {
			continue
		}

		absPath := filepath.Join(rootDir, file.RelPath)
		content, err := os.ReadFile(absPath)
		if err != nil {
			allWarnings = append(allWarnings, fmt.Sprintf("failed to read manifest %s: %v", file.RelPath, err))
			continue
		}

		res, err := registry.ParseManifest(file.RelPath, content)
		if err == nil && res != nil {
			manifestResults = append(manifestResults, res)
			if len(res.Warnings) > 0 {
				allWarnings = append(allWarnings, res.Warnings...)
			}
		}
	}

	// 2. Merge and sort all dependencies deterministically
	rawRuntime, rawDev := MergeDependencies(manifestResults)

	runtimeDeps := make([]engine.DependencyItem, 0, len(rawRuntime))
	allRawDeps := make([]DependencyRecord, 0, len(rawRuntime)+len(rawDev))
	for _, d := range rawRuntime {
		runtimeDeps = append(runtimeDeps, engine.DependencyItem{
			Name:    d.Name,
			Version: d.Version,
			Manager: d.Manager,
			IsDev:   false,
		})
		allRawDeps = append(allRawDeps, d)
	}

	devDeps := make([]engine.DependencyItem, 0, len(rawDev))
	for _, d := range rawDev {
		devDeps = append(devDeps, engine.DependencyItem{
			Name:    d.Name,
			Version: d.Version,
			Manager: d.Manager,
			IsDev:   true,
		})
		allRawDeps = append(allRawDeps, d)
	}

	// 3. Scan environment variables
	envScanner := NewEnvironmentScanner()
	envVars := envScanner.ScanEnvironmentVariables(rootDir, inv)

	envNames := make([]string, 0, len(envVars))
	for _, ev := range envVars {
		envNames = append(envNames, ev.Name)
	}

	// 4. Detect technologies and evaluate negative assertions
	technologies := DetectTechnologies(inv, allRawDeps, envNames)

	// 5. Extract static API routes
	routeExtractor := NewRouteExtractor()
	routes := routeExtractor.ExtractRoutes(rootDir, inv)

	// 6. Extract testing & deployment info
	testingInfo := ExtractTestingInfo(inv, manifestResults, technologies)
	deploymentInfo := ExtractDeploymentInfo(inv)

	// 7. Generate setup commands
	setupInfo := generateSetupInfo(manifestResults)

	return &AnalysisFacts{
		Manifests:    manifestResults,
		RuntimeDeps:  runtimeDeps,
		DevDeps:      devDeps,
		Technologies: technologies,
		Environment:  envVars,
		Routes:       routes,
		Testing:      testingInfo,
		Deployment:   deploymentInfo,
		Setup:        setupInfo,
		Warnings:     allWarnings,
	}, nil
}

func generateSetupInfo(manifests []*ManifestResult) engine.SetupInfo {
	prereqs := make([]string, 0)
	installCmds := make([]engine.SetupCommand, 0)
	runCmds := make([]engine.SetupCommand, 0)
	buildCmds := make([]engine.SetupCommand, 0)

	for _, m := range manifests {
		if m == nil {
			continue
		}
		switch m.Type {
		case ManifestPackageJSON:
			prereqs = append(prereqs, "Node.js >= 18", "npm / yarn / pnpm")
			installCmds = append(installCmds, engine.SetupCommand{Label: "Install Node dependencies", Cmd: "npm install"})
			if _, exists := m.Scripts["dev"]; exists {
				runCmds = append(runCmds, engine.SetupCommand{Label: "Start dev server", Cmd: "npm run dev"})
			} else if _, exists := m.Scripts["start"]; exists {
				runCmds = append(runCmds, engine.SetupCommand{Label: "Start application", Cmd: "npm start"})
			}
			if _, exists := m.Scripts["build"]; exists {
				buildCmds = append(buildCmds, engine.SetupCommand{Label: "Build production bundle", Cmd: "npm run build"})
			}
		case ManifestGoMod:
			prereqs = append(prereqs, "Go >= 1.22")
			installCmds = append(installCmds, engine.SetupCommand{Label: "Download Go modules", Cmd: "go mod download"})
			runCmds = append(runCmds, engine.SetupCommand{Label: "Run main application", Cmd: "go run ./..."})
			buildCmds = append(buildCmds, engine.SetupCommand{Label: "Compile binary", Cmd: "go build -o app ./..."})
		case ManifestCargoTOML:
			prereqs = append(prereqs, "Rust / Cargo (latest stable)")
			installCmds = append(installCmds, engine.SetupCommand{Label: "Fetch Rust crates", Cmd: "cargo fetch"})
			runCmds = append(runCmds, engine.SetupCommand{Label: "Run Rust binary", Cmd: "cargo run"})
			buildCmds = append(buildCmds, engine.SetupCommand{Label: "Build release binary", Cmd: "cargo build --release"})
		case ManifestRequirementsTxt, ManifestPyprojectTOML:
			prereqs = append(prereqs, "Python >= 3.11")
			installCmds = append(installCmds, engine.SetupCommand{Label: "Install Python packages", Cmd: "pip install -r requirements.txt"})
			runCmds = append(runCmds, engine.SetupCommand{Label: "Run Python application", Cmd: "python main.py"})
		case ManifestPomXML:
			prereqs = append(prereqs, "Java JDK >= 17", "Maven")
			installCmds = append(installCmds, engine.SetupCommand{Label: "Install Maven dependencies", Cmd: "mvn clean install"})
			runCmds = append(runCmds, engine.SetupCommand{Label: "Run Spring Boot app", Cmd: "mvn spring-boot:run"})
			buildCmds = append(buildCmds, engine.SetupCommand{Label: "Package JAR", Cmd: "mvn package"})
		}
	}

	sort.Strings(prereqs)

	return engine.SetupInfo{
		Prerequisites:   prereqs,
		InstallCommands: installCmds,
		RunCommands:     runCmds,
		BuildCommands:   buildCmds,
	}
}
