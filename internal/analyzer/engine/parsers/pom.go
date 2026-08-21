package parsers

import (
	"encoding/xml"
	"fmt"
	"sort"
	"strings"
)

type pomDependency struct {
	GroupID    string `xml:"groupId"`
	ArtifactID string `xml:"artifactId"`
	Version    string `xml:"version"`
	Scope      string `xml:"scope"`
	Optional   string `xml:"optional"`
}

type pomProject struct {
	XMLName      xml.Name        `xml:"project"`
	GroupID      string          `xml:"groupId"`
	ArtifactID   string          `xml:"artifactId"`
	Version      string          `xml:"version"`
	Dependencies []pomDependency `xml:"dependencies>dependency"`
}

// PomXMLParser parses Java Maven pom.xml manifest files.
type PomXMLParser struct{}

func (p *PomXMLParser) CanParse(filename string) bool {
	return strings.ToLower(filename) == "pom.xml"
}

func (p *PomXMLParser) Parse(relPath string, content []byte) (*ManifestResult, error) {
	var proj pomProject
	if err := xml.Unmarshal(content, &proj); err != nil {
		return &ManifestResult{
			Path:      relPath,
			Type:      ManifestPomXML,
			Ecosystem: "maven",
			Warnings:  []string{fmt.Sprintf("malformed pom.xml: %v", err)},
		}, nil
	}

	runtimeDeps := make([]DependencyRecord, 0, len(proj.Dependencies))
	devDeps := make([]DependencyRecord, 0)

	for _, dep := range proj.Dependencies {
		name := strings.TrimSpace(dep.GroupID + ":" + dep.ArtifactID)
		if name == ":" {
			continue
		}
		ver := strings.TrimSpace(dep.Version)
		scope := strings.ToLower(strings.TrimSpace(dep.Scope))
		isDev := scope == "test" || scope == "provided"

		record := DependencyRecord{
			Name:       name,
			Version:    ver,
			Manager:    "maven",
			IsDev:      isDev,
			SourcePath: relPath,
		}

		if isDev {
			devDeps = append(devDeps, record)
		} else {
			runtimeDeps = append(runtimeDeps, record)
		}
	}

	sort.Slice(runtimeDeps, func(i, j int) bool { return runtimeDeps[i].Name < runtimeDeps[j].Name })
	sort.Slice(devDeps, func(i, j int) bool { return devDeps[i].Name < devDeps[j].Name })

	projName := proj.ArtifactID
	if proj.GroupID != "" && proj.ArtifactID != "" {
		projName = proj.GroupID + ":" + proj.ArtifactID
	}

	return &ManifestResult{
		Path:        relPath,
		Type:        ManifestPomXML,
		Ecosystem:   "maven",
		ProjectName: projName,
		Version:     proj.Version,
		RuntimeDeps: runtimeDeps,
		DevDeps:     devDeps,
	}, nil
}
