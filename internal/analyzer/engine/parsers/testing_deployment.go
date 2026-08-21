package parsers

import (
	"path/filepath"
	"sort"
	"strings"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/inventory"
)

// ExtractTestingInfo aggregates test frameworks, test files, directories, and default runner commands.
func ExtractTestingInfo(
	inv *inventory.ScopeInventory,
	manifests []*ManifestResult,
	technologies []engine.TechnologyItem,
) engine.TestingInfo {
	frameworks := make([]string, 0)
	testDirsSet := make(map[string]struct{})
	testCommandsSet := make(map[string]struct{})
	testFileCount := 0

	for _, tech := range technologies {
		if tech.Category == engine.CategoryTesting {
			frameworks = append(frameworks, tech.Name)
		}
	}

	for _, file := range inv.Files {
		if file.IsExcluded {
			continue
		}
		if file.Category == inventory.CategoryTest {
			testFileCount++
			dir := filepath.Dir(file.RelPath)
			if dir != "." && dir != "" {
				testDirsSet[dir] = struct{}{}
			}
		}
	}

	// Extract standard test commands based on detected manifests
	for _, m := range manifests {
		if m == nil {
			continue
		}
		switch m.Type {
		case ManifestPackageJSON:
			if cmd, exists := m.Scripts["test"]; exists && cmd != "" {
				testCommandsSet["npm test"] = struct{}{}
			}
		case ManifestGoMod:
			testCommandsSet["go test ./..."] = struct{}{}
		case ManifestCargoTOML:
			testCommandsSet["cargo test"] = struct{}{}
		case ManifestRequirementsTxt, ManifestPyprojectTOML:
			testCommandsSet["pytest"] = struct{}{}
		case ManifestPomXML:
			testCommandsSet["mvn test"] = struct{}{}
		case ManifestComposerJSON:
			testCommandsSet["composer test"] = struct{}{}
		}
	}

	testDirs := make([]string, 0, len(testDirsSet))
	for d := range testDirsSet {
		testDirs = append(testDirs, d)
	}
	sort.Strings(testDirs)

	testCommands := make([]string, 0, len(testCommandsSet))
	for c := range testCommandsSet {
		testCommands = append(testCommands, c)
	}
	sort.Strings(testCommands)
	sort.Strings(frameworks)

	return engine.TestingInfo{
		Frameworks:      frameworks,
		TestCommands:    testCommands,
		TestDirectories: testDirs,
		TestFileCount:   testFileCount,
	}
}

// ExtractDeploymentInfo locates Dockerfiles, Compose files, and CI/CD workflows.
func ExtractDeploymentInfo(
	inv *inventory.ScopeInventory,
) engine.DeploymentInfo {
	dockerfiles := make([]string, 0)
	composeFiles := make([]string, 0)
	ciWorkflows := make([]engine.DeploymentCIWorkflow, 0)
	targetPlatformsSet := make(map[string]struct{})

	for _, file := range inv.Files {
		if file.IsExcluded {
			continue
		}
		norm := strings.ToLower(file.RelPath)
		base := filepath.Base(norm)

		if base == "dockerfile" || strings.HasPrefix(base, "dockerfile.") {
			dockerfiles = append(dockerfiles, file.RelPath)
			targetPlatformsSet["Docker"] = struct{}{}
		}

		if base == "docker-compose.yml" || base == "docker-compose.yaml" ||
			base == "compose.yml" || base == "compose.yaml" {
			composeFiles = append(composeFiles, file.RelPath)
			targetPlatformsSet["Docker Compose"] = struct{}{}
		}

		if strings.HasPrefix(norm, ".github/workflows/") && (strings.HasSuffix(norm, ".yml") || strings.HasSuffix(norm, ".yaml")) {
			ciWorkflows = append(ciWorkflows, engine.DeploymentCIWorkflow{
				Name:     filepath.Base(file.RelPath),
				Path:     file.RelPath,
				Triggers: []string{"push", "pull_request"},
			})
			targetPlatformsSet["GitHub Actions"] = struct{}{}
		}

		if base == ".gitlab-ci.yml" {
			ciWorkflows = append(ciWorkflows, engine.DeploymentCIWorkflow{
				Name:     "GitLab CI",
				Path:     file.RelPath,
				Triggers: []string{"pipeline"},
			})
			targetPlatformsSet["GitLab CI"] = struct{}{}
		}
	}

	sort.Strings(dockerfiles)
	sort.Strings(composeFiles)
	sort.Slice(ciWorkflows, func(i, j int) bool {
		return ciWorkflows[i].Path < ciWorkflows[j].Path
	})

	targetPlatforms := make([]string, 0, len(targetPlatformsSet))
	for p := range targetPlatformsSet {
		targetPlatforms = append(targetPlatforms, p)
	}
	sort.Strings(targetPlatforms)

	return engine.DeploymentInfo{
		DockerAvailable: len(dockerfiles) > 0 || len(composeFiles) > 0,
		DockerfilePaths: dockerfiles,
		ComposePaths:    composeFiles,
		CIWorkflows:     ciWorkflows,
		TargetPlatforms: targetPlatforms,
	}
}
