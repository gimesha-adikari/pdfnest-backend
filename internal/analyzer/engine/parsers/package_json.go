package parsers

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type rawPackageJSON struct {
	Name             string            `json:"name"`
	Version          string            `json:"version"`
	License          interface{}       `json:"license"`
	Scripts          map[string]string `json:"scripts"`
	Dependencies     map[string]string `json:"dependencies"`
	DevDependencies  map[string]string `json:"devDependencies"`
	PeerDependencies map[string]string `json:"peerDependencies"`
}

// PackageJSONParser parses Node.js/JavaScript/TypeScript package.json files.
type PackageJSONParser struct{}

func (p *PackageJSONParser) CanParse(filename string) bool {
	return filename == "package.json"
}

func (p *PackageJSONParser) Parse(relPath string, content []byte) (*ManifestResult, error) {
	var raw rawPackageJSON
	warnings := make([]string, 0)

	if err := json.Unmarshal(content, &raw); err != nil {
		return &ManifestResult{
			Path:      relPath,
			Type:      ManifestPackageJSON,
			Ecosystem: "npm",
			Warnings:  []string{fmt.Sprintf("malformed package.json: %v", err)},
		}, nil
	}

	runtimeDeps := make([]DependencyRecord, 0, len(raw.Dependencies)+len(raw.PeerDependencies))
	for name, ver := range raw.Dependencies {
		runtimeDeps = append(runtimeDeps, DependencyRecord{
			Name:       name,
			Version:    strings.TrimSpace(ver),
			Manager:    "npm",
			IsDev:      false,
			SourcePath: relPath,
		})
	}
	for name, ver := range raw.PeerDependencies {
		runtimeDeps = append(runtimeDeps, DependencyRecord{
			Name:       name,
			Version:    strings.TrimSpace(ver),
			Manager:    "npm",
			IsDev:      false,
			SourcePath: relPath,
		})
	}

	devDeps := make([]DependencyRecord, 0, len(raw.DevDependencies))
	for name, ver := range raw.DevDependencies {
		devDeps = append(devDeps, DependencyRecord{
			Name:       name,
			Version:    strings.TrimSpace(ver),
			Manager:    "npm",
			IsDev:      true,
			SourcePath: relPath,
		})
	}

	sort.Slice(runtimeDeps, func(i, j int) bool { return runtimeDeps[i].Name < runtimeDeps[j].Name })
	sort.Slice(devDeps, func(i, j int) bool { return devDeps[i].Name < devDeps[j].Name })

	return &ManifestResult{
		Path:        relPath,
		Type:        ManifestPackageJSON,
		Ecosystem:   "npm",
		ProjectName: raw.Name,
		Version:     raw.Version,
		RuntimeDeps: runtimeDeps,
		DevDeps:     devDeps,
		Scripts:     raw.Scripts,
		Warnings:    warnings,
	}, nil
}
