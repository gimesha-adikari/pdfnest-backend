package parsers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type rawComposerJSON struct {
	Name        string                 `json:"name"`
	Version     string                 `json:"version"`
	Description string                 `json:"description"`
	Require     map[string]string      `json:"require"`
	RequireDev  map[string]string      `json:"require-dev"`
	Scripts     map[string]interface{} `json:"scripts"`
}

// ComposerJSONParser parses PHP composer.json manifest files.
type ComposerJSONParser struct{}

func (p *ComposerJSONParser) CanParse(filename string) bool {
	return strings.ToLower(filename) == "composer.json"
}

func (p *ComposerJSONParser) Parse(relPath string, content []byte) (*ManifestResult, error) {
	var raw rawComposerJSON
	if err := json.Unmarshal(content, &raw); err != nil {
		return &ManifestResult{
			Path:      relPath,
			Type:      ManifestComposerJSON,
			Ecosystem: "composer",
			Warnings:  []string{fmt.Sprintf("malformed composer.json: %v", err)},
		}, nil
	}

	runtimeDeps := make([]DependencyRecord, 0, len(raw.Require))
	devDeps := make([]DependencyRecord, 0, len(raw.RequireDev))

	for name, ver := range raw.Require {
		if name == "php" || strings.HasPrefix(name, "ext-") {
			continue
		}
		runtimeDeps = append(runtimeDeps, DependencyRecord{
			Name:       name,
			Version:    strings.TrimSpace(ver),
			Manager:    "composer",
			IsDev:      false,
			SourcePath: relPath,
		})
	}

	for name, ver := range raw.RequireDev {
		if name == "php" || strings.HasPrefix(name, "ext-") {
			continue
		}
		devDeps = append(devDeps, DependencyRecord{
			Name:       name,
			Version:    strings.TrimSpace(ver),
			Manager:    "composer",
			IsDev:      true,
			SourcePath: relPath,
		})
	}

	sort.Slice(runtimeDeps, func(i, j int) bool { return runtimeDeps[i].Name < runtimeDeps[j].Name })
	sort.Slice(devDeps, func(i, j int) bool { return devDeps[i].Name < devDeps[j].Name })

	return &ManifestResult{
		Path:        relPath,
		Type:        ManifestComposerJSON,
		Ecosystem:   "composer",
		ProjectName: raw.Name,
		Version:     raw.Version,
		RuntimeDeps: runtimeDeps,
		DevDeps:     devDeps,
	}, nil
}
