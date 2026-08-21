package parsers

import (
	"bufio"
	"bytes"
	"regexp"
	"sort"
	"strings"
)

// CargoTOMLParser parses Rust Cargo.toml manifest files.
type CargoTOMLParser struct{}

func (p *CargoTOMLParser) CanParse(filename string) bool {
	return filename == "cargo.toml"
}

func (p *CargoTOMLParser) Parse(relPath string, content []byte) (*ManifestResult, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))

	var currentSection string
	var projectName string
	var projectVersion string
	runtimeDeps := make([]DependencyRecord, 0)
	devDeps := make([]DependencyRecord, 0)

	reDepSimple := regexp.MustCompile(`^([a-zA-Z0-9_-]+)\s*=\s*"([^"]+)"`)
	reDepTable := regexp.MustCompile(`^([a-zA-Z0-9_-]+)\s*=\s*\{.*version\s*=\s*"([^"]+)".*\}`)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.ToLower(strings.Trim(line, "[] \t"))
			continue
		}

		if currentSection == "package" {
			if strings.HasPrefix(line, "name") && strings.Contains(line, "=") {
				parts := strings.SplitN(line, "=", 2)
				projectName = strings.Trim(strings.TrimSpace(parts[1]), "\" \t")
			}
			if strings.HasPrefix(line, "version") && strings.Contains(line, "=") {
				parts := strings.SplitN(line, "=", 2)
				projectVersion = strings.Trim(strings.TrimSpace(parts[1]), "\" \t")
			}
			continue
		}

		if currentSection == "dependencies" || strings.HasPrefix(currentSection, "dependencies.") {
			if m := reDepTable.FindStringSubmatch(line); len(m) == 3 {
				runtimeDeps = append(runtimeDeps, DependencyRecord{
					Name:       m[1],
					Version:    m[2],
					Manager:    "cargo",
					IsDev:      false,
					SourcePath: relPath,
				})
			} else if m := reDepSimple.FindStringSubmatch(line); len(m) == 3 {
				runtimeDeps = append(runtimeDeps, DependencyRecord{
					Name:       m[1],
					Version:    m[2],
					Manager:    "cargo",
					IsDev:      false,
					SourcePath: relPath,
				})
			}
		}

		if currentSection == "dev-dependencies" || strings.HasPrefix(currentSection, "dev-dependencies.") ||
			currentSection == "build-dependencies" {
			if m := reDepTable.FindStringSubmatch(line); len(m) == 3 {
				devDeps = append(devDeps, DependencyRecord{
					Name:       m[1],
					Version:    m[2],
					Manager:    "cargo",
					IsDev:      true,
					SourcePath: relPath,
				})
			} else if m := reDepSimple.FindStringSubmatch(line); len(m) == 3 {
				devDeps = append(devDeps, DependencyRecord{
					Name:       m[1],
					Version:    m[2],
					Manager:    "cargo",
					IsDev:      true,
					SourcePath: relPath,
				})
			}
		}
	}

	sort.Slice(runtimeDeps, func(i, j int) bool { return runtimeDeps[i].Name < runtimeDeps[j].Name })
	sort.Slice(devDeps, func(i, j int) bool { return devDeps[i].Name < devDeps[j].Name })

	return &ManifestResult{
		Path:        relPath,
		Type:        ManifestCargoTOML,
		Ecosystem:   "cargo",
		ProjectName: projectName,
		Version:     projectVersion,
		RuntimeDeps: runtimeDeps,
		DevDeps:     devDeps,
	}, nil
}
