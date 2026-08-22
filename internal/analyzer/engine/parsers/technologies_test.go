package parsers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/inventory"
)

func TestDetectTechnologiesWithNegativeAssertions(t *testing.T) {
	inv := &inventory.ScopeInventory{
		LanguagesFound: []string{"TypeScript", "Go"},
		Files: []inventory.FileEntry{
			{RelPath: "src/main.ts", Category: inventory.CategorySource},
			{RelPath: "Dockerfile", Category: inventory.CategoryConfig},
			{RelPath: "prisma/schema.prisma", Category: inventory.CategoryConfig},
		},
	}

	manifestDeps := []DependencyRecord{
		{Name: "next", Version: "14.2.0", Manager: "npm", SourcePath: "package.json"},
		{Name: "@prisma/client", Version: "5.12.0", Manager: "npm", SourcePath: "package.json"},
		{Name: "pg", Version: "8.11.0", Manager: "npm", SourcePath: "package.json"},
		{Name: "ioredis", Version: "5.3.2", Manager: "npm", SourcePath: "package.json"},
	}

	envVarNames := []string{"DATABASE_URL", "REDIS_URL"}

	techs := DetectTechnologies(inv, manifestDeps, envVarNames)
	assert.NotEmpty(t, techs)

	techMap := make(map[string]engine.TechnologyItem)
	for _, tech := range techs {
		techMap[tech.Name] = tech
	}

	// Verify Positive Detections
	assert.Contains(t, techMap, "Next.js")
	assert.Equal(t, engine.ConfidenceConfirmed, techMap["Next.js"].Confidence)

	assert.Contains(t, techMap, "Prisma")
	assert.Equal(t, engine.ConfidenceConfirmed, techMap["Prisma"].Confidence)

	assert.Contains(t, techMap, "PostgreSQL")
	assert.Equal(t, engine.ConfidenceConfirmed, techMap["PostgreSQL"].Confidence)

	assert.Contains(t, techMap, "Redis")
	assert.Equal(t, engine.ConfidenceConfirmed, techMap["Redis"].Confidence)

	assert.Contains(t, techMap, "Docker")
	assert.Equal(t, engine.ConfidenceConfirmed, techMap["Docker"].Confidence)

	// Verify Negative Assertions
	// Next.js detected -> Nuxt, Remix, Gatsby must be in negative assertions
	assert.Contains(t, techMap["Next.js"].NegativeAssertionsPassed, "Nuxt")

	// PostgreSQL detected -> MySQL, MongoDB, SQLite must be in negative assertions
	assert.Contains(t, techMap["PostgreSQL"].NegativeAssertionsPassed, "MongoDB")
	assert.Contains(t, techMap["PostgreSQL"].NegativeAssertionsPassed, "MySQL")
	assert.Contains(t, techMap["PostgreSQL"].NegativeAssertionsPassed, "SQLite")

	// Redis detected -> Memcached, Kafka must be in negative assertions
	assert.Contains(t, techMap["Redis"].NegativeAssertionsPassed, "Memcached")
	assert.Contains(t, techMap["Redis"].NegativeAssertionsPassed, "Kafka")
}

func TestDetectTechnologiesCanonicalEvidence(t *testing.T) {
	inv := &inventory.ScopeInventory{
		LanguagesFound: []string{"TypeScript"},
		Files: []inventory.FileEntry{
			{RelPath: "next.config.js", Category: inventory.CategoryConfig},
			{RelPath: ".env.example", Category: inventory.CategoryConfig},
		},
	}

	manifestDeps := []DependencyRecord{
		{Name: "next", Version: "14.2.0", Manager: "npm", SourcePath: "package.json"},
	}

	envVarNames := []string{"POSTGRES_PASSWORD"}

	techs := DetectTechnologies(inv, manifestDeps, envVarNames)
	assert.NotEmpty(t, techs)

	techMap := make(map[string]engine.TechnologyItem)
	for _, tech := range techs {
		techMap[tech.Name] = tech
	}

	// Verify Canonical Evidence
	nextjs, found := techMap["Next.js"]
	assert.True(t, found)
	assert.NotEmpty(t, nextjs.CanonicalEvidence)

	var hasConfirmed, hasStronglyInferred bool
	for _, ev := range nextjs.CanonicalEvidence {
		if ev.Confidence == engine.EpistemicConfidenceConfirmed && ev.SourceType == "manifest" {
			hasConfirmed = true
		}
		if ev.Confidence == engine.EpistemicConfidenceStronglyInferred && ev.SourceType == "config" {
			hasStronglyInferred = true
		}
	}
	assert.True(t, hasConfirmed, "Expected Confirmed evidence for manifest")
	assert.True(t, hasStronglyInferred, "Expected StronglyInferred evidence for config")

	postgres, found := techMap["PostgreSQL"]
	assert.True(t, found)
	assert.NotEmpty(t, postgres.CanonicalEvidence)
	assert.Equal(t, engine.EpistemicConfidenceWeaklyInferred, postgres.CanonicalEvidence[0].Confidence)
}
