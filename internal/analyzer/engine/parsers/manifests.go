package parsers

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// ManifestType identifies the specification of the dependency manifest file.
type ManifestType string

const (
	ManifestPackageJSON     ManifestType = "package.json"
	ManifestGoMod           ManifestType = "go.mod"
	ManifestCargoTOML       ManifestType = "Cargo.toml"
	ManifestRequirementsTxt ManifestType = "requirements.txt"
	ManifestPyprojectTOML   ManifestType = "pyproject.toml"
	ManifestPomXML          ManifestType = "pom.xml"
	ManifestComposerJSON    ManifestType = "composer.json"
)

// DependencyRecord represents a single declared package dependency extracted from a manifest.
type DependencyRecord struct {
	Name       string `json:"name"`
	Version    string `json:"version"`
	Manager    string `json:"manager"`
	IsDev      bool   `json:"isDev"`
	SourcePath string `json:"sourcePath"`
	License    string `json:"license,omitempty"`
}

// ManifestResult contains structured dependencies and metadata extracted from a manifest.
type ManifestResult struct {
	Path        string             `json:"path"`
	Type        ManifestType       `json:"type"`
	Ecosystem   string             `json:"ecosystem"`
	ProjectName string             `json:"projectName,omitempty"`
	Version     string             `json:"version,omitempty"`
	RuntimeDeps []DependencyRecord `json:"runtimeDeps"`
	DevDeps     []DependencyRecord `json:"devDeps"`
	Scripts     map[string]string  `json:"scripts,omitempty"`
	Warnings    []string           `json:"warnings,omitempty"`
}

// ManifestParser defines the interface implemented by each deterministic manifest parser.
type ManifestParser interface {
	CanParse(filename string) bool
	Parse(relPath string, content []byte) (*ManifestResult, error)
}

// Registry manages all available manifest parsers.
type Registry struct {
	parsers []ManifestParser
}

// NewRegistry initializes the registry with all standard Phase 3 manifest parsers.
func NewRegistry() *Registry {
	r := &Registry{
		parsers: make([]ManifestParser, 0, 7),
	}
	r.Register(&PackageJSONParser{})
	r.Register(&GoModParser{})
	r.Register(&CargoTOMLParser{})
	r.Register(&RequirementsTxtParser{})
	r.Register(&PyprojectTOMLParser{})
	r.Register(&PomXMLParser{})
	r.Register(&ComposerJSONParser{})
	return r
}

// Register adds a manifest parser to the registry.
func (r *Registry) Register(p ManifestParser) {
	r.parsers = append(r.parsers, p)
}

// ParseManifest dispatches manifest parsing based on filename.
func (r *Registry) ParseManifest(relPath string, content []byte) (*ManifestResult, error) {
	base := filepath.Base(relPath)
	lowerBase := strings.ToLower(base)

	for _, p := range r.parsers {
		if p.CanParse(lowerBase) {
			return p.Parse(relPath, content)
		}
	}

	return nil, fmt.Errorf("no registered parser for manifest: %s", relPath)
}

// MergeDependencies deterministically aggregates dependencies across multiple manifests,
// deduplicating entries while preserving source provenance.
func MergeDependencies(results []*ManifestResult) ([]DependencyRecord, []DependencyRecord) {
	runtimeMap := make(map[string]DependencyRecord)
	devMap := make(map[string]DependencyRecord)

	for _, res := range results {
		if res == nil {
			continue
		}
		for _, dep := range res.RuntimeDeps {
			key := fmt.Sprintf("%s:%s", dep.Manager, dep.Name)
			if existing, exists := runtimeMap[key]; exists {
				// Deterministic merge: prefer direct/root source or alphabetically earlier
				if dep.SourcePath < existing.SourcePath {
					runtimeMap[key] = dep
				}
			} else {
				runtimeMap[key] = dep
			}
		}
		for _, dep := range res.DevDeps {
			key := fmt.Sprintf("%s:%s", dep.Manager, dep.Name)
			if existing, exists := devMap[key]; exists {
				if dep.SourcePath < existing.SourcePath {
					devMap[key] = dep
				}
			} else {
				devMap[key] = dep
			}
		}
	}

	runtimeList := make([]DependencyRecord, 0, len(runtimeMap))
	for _, dep := range runtimeMap {
		runtimeList = append(runtimeList, dep)
	}
	sort.Slice(runtimeList, func(i, j int) bool {
		if runtimeList[i].Name == runtimeList[j].Name {
			return runtimeList[i].SourcePath < runtimeList[j].SourcePath
		}
		return runtimeList[i].Name < runtimeList[j].Name
	})

	devList := make([]DependencyRecord, 0, len(devMap))
	for _, dep := range devMap {
		devList = append(devList, dep)
	}
	sort.Slice(devList, func(i, j int) bool {
		if devList[i].Name == devList[j].Name {
			return devList[i].SourcePath < devList[j].SourcePath
		}
		return devList[i].Name < devList[j].Name
	})

	return runtimeList, devList
}
