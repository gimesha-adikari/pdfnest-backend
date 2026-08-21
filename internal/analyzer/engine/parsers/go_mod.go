package parsers

import (
	"bufio"
	"bytes"
	"sort"
	"strings"
)

// GoModParser parses Go go.mod files.
type GoModParser struct{}

func (p *GoModParser) CanParse(filename string) bool {
	return filename == "go.mod"
}

func (p *GoModParser) Parse(relPath string, content []byte) (*ManifestResult, error) {
	scanner := bufio.NewScanner(bytes.NewReader(content))

	var moduleName string
	var goVersion string
	runtimeDeps := make([]DependencyRecord, 0)
	devDeps := make([]DependencyRecord, 0)
	inRequireBlock := false

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "//") {
			continue
		}

		if strings.HasPrefix(line, "module ") {
			moduleName = strings.TrimSpace(strings.TrimPrefix(line, "module "))
			continue
		}

		if strings.HasPrefix(line, "go ") {
			goVersion = strings.TrimSpace(strings.TrimPrefix(line, "go "))
			continue
		}

		if line == "require (" {
			inRequireBlock = true
			continue
		}

		if inRequireBlock && line == ")" {
			inRequireBlock = false
			continue
		}

		if inRequireBlock {
			parseRequireLine(line, relPath, &runtimeDeps, &devDeps)
			continue
		}

		if strings.HasPrefix(line, "require ") {
			reqBody := strings.TrimSpace(strings.TrimPrefix(line, "require "))
			parseRequireLine(reqBody, relPath, &runtimeDeps, &devDeps)
		}
	}

	sort.Slice(runtimeDeps, func(i, j int) bool { return runtimeDeps[i].Name < runtimeDeps[j].Name })
	sort.Slice(devDeps, func(i, j int) bool { return devDeps[i].Name < devDeps[j].Name })

	return &ManifestResult{
		Path:        relPath,
		Type:        ManifestGoMod,
		Ecosystem:   "go",
		ProjectName: moduleName,
		Version:     goVersion,
		RuntimeDeps: runtimeDeps,
		DevDeps:     devDeps,
	}, nil
}

func parseRequireLine(line string, sourcePath string, runtimeDeps *[]DependencyRecord, devDeps *[]DependencyRecord) {
	isIndirect := strings.Contains(line, "// indirect")
	cleanLine := strings.TrimSpace(strings.Split(line, "//")[0])
	parts := strings.Fields(cleanLine)
	if len(parts) >= 2 {
		name := parts[0]
		ver := parts[1]
		record := DependencyRecord{
			Name:       name,
			Version:    ver,
			Manager:    "go",
			IsDev:      isIndirect,
			SourcePath: sourcePath,
		}
		if isIndirect {
			*devDeps = append(*devDeps, record)
		} else {
			*runtimeDeps = append(*runtimeDeps, record)
		}
	}
}
