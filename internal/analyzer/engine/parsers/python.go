package parsers

import (
	"bufio"
	"bytes"
	"regexp"
	"sort"
	"strings"
)

// RequirementsTxtParser parses Python requirements.txt files.
type RequirementsTxtParser struct{}

func (p *RequirementsTxtParser) CanParse(filename string) bool {
	lower := strings.ToLower(filename)
	return lower == "requirements.txt" ||
		lower == "requirements-dev.txt" ||
		lower == "requirements_dev.txt" ||
		strings.HasPrefix(lower, "requirements-") ||
		strings.HasPrefix(lower, "requirements/")
}

func (p *RequirementsTxtParser) Parse(relPath string, content []byte) (*ManifestResult, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))
	isDevManifest := strings.Contains(strings.ToLower(relPath), "dev") ||
		strings.Contains(strings.ToLower(relPath), "test")

	runtimeDeps := make([]DependencyRecord, 0)
	devDeps := make([]DependencyRecord, 0)

	rePkg := regexp.MustCompile(`^([a-zA-Z0-9_.-]+)(\[.*\])?\s*([=><~^!].*)?$`)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}

		// Strip inline comments
		line = strings.TrimSpace(strings.Split(line, "#")[0])
		if line == "" {
			continue
		}

		if m := rePkg.FindStringSubmatch(line); len(m) >= 2 {
			pkgName := m[1]
			version := ""
			if len(m) >= 4 {
				version = strings.TrimSpace(m[3])
			}

			record := DependencyRecord{
				Name:       pkgName,
				Version:    version,
				Manager:    "pip",
				IsDev:      isDevManifest,
				SourcePath: relPath,
			}

			if isDevManifest {
				devDeps = append(devDeps, record)
			} else {
				runtimeDeps = append(runtimeDeps, record)
			}
		}
	}

	sort.Slice(runtimeDeps, func(i, j int) bool { return runtimeDeps[i].Name < runtimeDeps[j].Name })
	sort.Slice(devDeps, func(i, j int) bool { return devDeps[i].Name < devDeps[j].Name })

	return &ManifestResult{
		Path:        relPath,
		Type:        ManifestRequirementsTxt,
		Ecosystem:   "pip",
		RuntimeDeps: runtimeDeps,
		DevDeps:     devDeps,
	}, nil
}

// PyprojectTOMLParser parses Python pyproject.toml files (PEP 621, Poetry, Flit).
type PyprojectTOMLParser struct{}

func (p *PyprojectTOMLParser) CanParse(filename string) bool {
	return filename == "pyproject.toml"
}

func (p *PyprojectTOMLParser) Parse(relPath string, content []byte) (*ManifestResult, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))

	var currentSection string
	var projectName string
	var projectVersion string
	runtimeDeps := make([]DependencyRecord, 0)
	devDeps := make([]DependencyRecord, 0)

	rePoetryDep := regexp.MustCompile(`^([a-zA-Z0-9_.-]+)\s*=\s*"([^"]+)"`)
	rePep621Dep := regexp.MustCompile(`^"([a-zA-Z0-9_.-]+)(\[.*\])?\s*([=><~^!].*)?"`)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			currentSection = strings.ToLower(strings.Trim(line, "[] \t"))
			continue
		}

		if currentSection == "project" || currentSection == "tool.poetry" {
			if strings.HasPrefix(line, "name") && strings.Contains(line, "=") {
				parts := strings.SplitN(line, "=", 2)
				projectName = strings.Trim(strings.TrimSpace(parts[1]), "\" \t")
			}
			if strings.HasPrefix(line, "version") && strings.Contains(line, "=") {
				parts := strings.SplitN(line, "=", 2)
				projectVersion = strings.Trim(strings.TrimSpace(parts[1]), "\" \t")
			}
		}

		if currentSection == "tool.poetry.dependencies" {
			if m := rePoetryDep.FindStringSubmatch(line); len(m) == 3 {
				if m[1] == "python" {
					continue
				}
				runtimeDeps = append(runtimeDeps, DependencyRecord{
					Name:       m[1],
					Version:    m[2],
					Manager:    "poetry",
					IsDev:      false,
					SourcePath: relPath,
				})
			}
		}

		if currentSection == "tool.poetry.group.dev.dependencies" ||
			currentSection == "tool.poetry.dev-dependencies" {
			if m := rePoetryDep.FindStringSubmatch(line); len(m) == 3 {
				devDeps = append(devDeps, DependencyRecord{
					Name:       m[1],
					Version:    m[2],
					Manager:    "poetry",
					IsDev:      true,
					SourcePath: relPath,
				})
			}
		}

		if currentSection == "project.dependencies" || currentSection == "project.optional-dependencies" {
			if m := rePep621Dep.FindStringSubmatch(line); len(m) >= 2 {
				pkgName := m[1]
				version := ""
				if len(m) >= 4 {
					version = strings.TrimSpace(m[3])
				}
				runtimeDeps = append(runtimeDeps, DependencyRecord{
					Name:       pkgName,
					Version:    version,
					Manager:    "pip",
					IsDev:      false,
					SourcePath: relPath,
				})
			}
		}
	}

	sort.Slice(runtimeDeps, func(i, j int) bool { return runtimeDeps[i].Name < runtimeDeps[j].Name })
	sort.Slice(devDeps, func(i, j int) bool { return devDeps[i].Name < devDeps[j].Name })

	return &ManifestResult{
		Path:        relPath,
		Type:        ManifestPyprojectTOML,
		Ecosystem:   "pip",
		ProjectName: projectName,
		Version:     projectVersion,
		RuntimeDeps: runtimeDeps,
		DevDeps:     devDeps,
	}, nil
}
