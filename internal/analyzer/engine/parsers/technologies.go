package parsers

import (
	"path/filepath"
	"sort"
	"strings"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/inventory"
)

// EvidenceMatcher maps a technology ID and category to concrete detection rules.
type EvidenceMatcher struct {
	ID                   string
	Name                 string
	Category             engine.TechnologyCategory
	ManifestDependencies []string // package names in manifests
	ConfigFiles          []string // file names like Dockerfile, next.config.js
	FileGlobs            []string // path globs like prisma/*.prisma
	SourceImports        []string // package import prefixes in source code
	EnvKeywords          []string // env variable names like REDIS_URL
	NegativeCheckAgainst []string // list of competing tech names to assert absent if not found
}

// RuleCatalog returns the standard, authoritative technology detection catalog.
func RuleCatalog() []EvidenceMatcher {
	return []EvidenceMatcher{
		// Frontend Frameworks & Tools
		{
			ID:                   "react",
			Name:                 "React",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"react", "react-dom"},
			NegativeCheckAgainst: []string{"Vue", "Svelte", "Angular"},
		},
		{
			ID:                   "nextjs",
			Name:                 "Next.js",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"next"},
			ConfigFiles:          []string{"next.config.js", "next.config.mjs", "next.config.ts"},
			NegativeCheckAgainst: []string{"Nuxt", "Remix", "Gatsby"},
		},
		{
			ID:                   "vue",
			Name:                 "Vue",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"vue"},
			NegativeCheckAgainst: []string{"React", "Svelte"},
		},
		{
			ID:                   "nuxt",
			Name:                 "Nuxt",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"nuxt"},
			ConfigFiles:          []string{"nuxt.config.js", "nuxt.config.ts"},
			NegativeCheckAgainst: []string{"Next.js"},
		},
		{
			ID:                   "svelte",
			Name:                 "Svelte",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"svelte", "@sveltejs/kit"},
			ConfigFiles:          []string{"svelte.config.js"},
			NegativeCheckAgainst: []string{"React", "Vue"},
		},
		{
			ID:                   "vite",
			Name:                 "Vite",
			Category:             engine.CategoryBuildTool,
			ManifestDependencies: []string{"vite", "@vitejs/plugin-react", "@vitejs/plugin-vue"},
			ConfigFiles:          []string{"vite.config.js", "vite.config.ts", "vite.config.mjs"},
			NegativeCheckAgainst: []string{"Webpack", "Turbopack"},
		},

		// Backend Frameworks
		{
			ID:                   "express",
			Name:                 "Express",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"express"},
			NegativeCheckAgainst: []string{"Fastify", "NestJS", "Koa"},
		},
		{
			ID:                   "fastify",
			Name:                 "Fastify",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"fastify"},
			NegativeCheckAgainst: []string{"Express"},
		},
		{
			ID:                   "nestjs",
			Name:                 "NestJS",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"@nestjs/core"},
			NegativeCheckAgainst: []string{"Express", "Fastify"},
		},
		{
			ID:                   "fastapi",
			Name:                 "FastAPI",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"fastapi"},
			NegativeCheckAgainst: []string{"Flask", "Django", "Tornado"},
		},
		{
			ID:                   "flask",
			Name:                 "Flask",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"flask"},
			NegativeCheckAgainst: []string{"FastAPI", "Django"},
		},
		{
			ID:                   "django",
			Name:                 "Django",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"django"},
			ConfigFiles:          []string{"manage.py"},
			NegativeCheckAgainst: []string{"FastAPI", "Flask"},
		},
		{
			ID:                   "fiber",
			Name:                 "Fiber",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"github.com/gofiber/fiber/v2", "github.com/gofiber/fiber"},
			NegativeCheckAgainst: []string{"Gin", "Echo", "Chi"},
		},
		{
			ID:                   "gin",
			Name:                 "Gin",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"github.com/gin-gonic/gin"},
			NegativeCheckAgainst: []string{"Fiber", "Echo"},
		},
		{
			ID:                   "echo",
			Name:                 "Echo",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"github.com/labstack/echo/v4", "github.com/labstack/echo"},
			NegativeCheckAgainst: []string{"Fiber", "Gin"},
		},
		{
			ID:                   "springboot",
			Name:                 "Spring Boot",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"org.springframework.boot:spring-boot-starter-web", "org.springframework.boot:spring-boot"},
			NegativeCheckAgainst: []string{"Micronaut", "Quarkus"},
		},

		// Databases
		{
			ID:                   "postgresql",
			Name:                 "PostgreSQL",
			Category:             engine.CategoryDatabase,
			ManifestDependencies: []string{"pg", "pgx", "psycopg2", "asyncpg", "github.com/lib/pq", "github.com/jackc/pgx/v5"},
			EnvKeywords:          []string{"POSTGRES_PASSWORD", "POSTGRES_USER", "POSTGRES_DB"},
			NegativeCheckAgainst: []string{"MySQL", "MongoDB", "SQLite"},
		},
		{
			ID:                   "mysql",
			Name:                 "MySQL",
			Category:             engine.CategoryDatabase,
			ManifestDependencies: []string{"mysql", "mysql2", "pymysql", "github.com/go-sql-driver/mysql"},
			EnvKeywords:          []string{"MYSQL_PASSWORD", "MYSQL_DATABASE"},
			NegativeCheckAgainst: []string{"PostgreSQL", "MongoDB", "SQLite"},
		},
		{
			ID:                   "sqlite",
			Name:                 "SQLite",
			Category:             engine.CategoryDatabase,
			ManifestDependencies: []string{"sqlite3", "better-sqlite3", "github.com/mattn/go-sqlite3"},
			NegativeCheckAgainst: []string{"PostgreSQL", "MySQL", "MongoDB"},
		},
		{
			ID:                   "mongodb",
			Name:                 "MongoDB",
			Category:             engine.CategoryDatabase,
			ManifestDependencies: []string{"mongodb", "mongoose", "pymongo", "go.mongodb.org/mongo-driver"},
			EnvKeywords:          []string{"MONGO_URI", "MONGODB_URI"},
			NegativeCheckAgainst: []string{"PostgreSQL", "MySQL", "SQLite"},
		},

		// Cache & Messaging
		{
			ID:                   "redis",
			Name:                 "Redis",
			Category:             engine.CategoryCache,
			ManifestDependencies: []string{"ioredis", "redis", "redis-py", "github.com/redis/go-redis/v9", "github.com/go-redis/redis"},
			EnvKeywords:          []string{"REDIS_URL", "REDIS_HOST"},
			NegativeCheckAgainst: []string{"Memcached", "RabbitMQ", "Kafka"},
		},
		{
			ID:                   "rabbitmq",
			Name:                 "RabbitMQ",
			Category:             engine.CategoryCache,
			ManifestDependencies: []string{"amqplib", "pika", "github.com/streadway/amqp"},
			EnvKeywords:          []string{"RABBITMQ_URL"},
			NegativeCheckAgainst: []string{"Kafka", "Redis"},
		},
		{
			ID:                   "kafka",
			Name:                 "Kafka",
			Category:             engine.CategoryCache,
			ManifestDependencies: []string{"kafkajs", "confluent-kafka", "github.com/segmentio/kafka-go"},
			EnvKeywords:          []string{"KAFKA_BROKERS"},
			NegativeCheckAgainst: []string{"RabbitMQ"},
		},

		// ORM & Data Access
		{
			ID:                   "prisma",
			Name:                 "Prisma",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"@prisma/client", "prisma"},
			ConfigFiles:          []string{"schema.prisma"},
			FileGlobs:            []string{"prisma/schema.prisma"},
			NegativeCheckAgainst: []string{"TypeORM", "Drizzle", "Mongoose"},
		},
		{
			ID:                   "drizzle",
			Name:                 "Drizzle",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"drizzle-orm"},
			ConfigFiles:          []string{"drizzle.config.ts", "drizzle.config.js"},
			NegativeCheckAgainst: []string{"Prisma", "TypeORM"},
		},
		{
			ID:                   "gorm",
			Name:                 "GORM",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"gorm.io/gorm"},
			NegativeCheckAgainst: []string{"sqlx", "ent"},
		},
		{
			ID:                   "sqlalchemy",
			Name:                 "SQLAlchemy",
			Category:             engine.CategoryFramework,
			ManifestDependencies: []string{"sqlalchemy"},
			NegativeCheckAgainst: []string{"Django ORM", "Peewee"},
		},

		// Infrastructure
		{
			ID:                   "docker",
			Name:                 "Docker",
			Category:             engine.CategoryInfrastructure,
			ConfigFiles:          []string{"Dockerfile", ".dockerignore"},
			NegativeCheckAgainst: []string{"Podman"},
		},
		{
			ID:                   "docker-compose",
			Name:                 "Docker Compose",
			Category:             engine.CategoryInfrastructure,
			ConfigFiles:          []string{"docker-compose.yml", "docker-compose.yaml", "compose.yml", "compose.yaml"},
			NegativeCheckAgainst: []string{"Kubernetes"},
		},
		{
			ID:                   "kubernetes",
			Name:                 "Kubernetes",
			Category:             engine.CategoryInfrastructure,
			FileGlobs:            []string{"k8s/*.yaml", "kubernetes/*.yaml"},
			NegativeCheckAgainst: []string{"Docker Compose"},
		},
		{
			ID:                   "github-actions",
			Name:                 "GitHub Actions",
			Category:             engine.CategoryInfrastructure,
			FileGlobs:            []string{".github/workflows/*.yml", ".github/workflows/*.yaml"},
			NegativeCheckAgainst: []string{"GitLab CI", "CircleCI", "Jenkins"},
		},
		{
			ID:                   "gitlab-ci",
			Name:                 "GitLab CI",
			Category:             engine.CategoryInfrastructure,
			ConfigFiles:          []string{".gitlab-ci.yml"},
			NegativeCheckAgainst: []string{"GitHub Actions"},
		},

		// Testing
		{
			ID:                   "jest",
			Name:                 "Jest",
			Category:             engine.CategoryTesting,
			ManifestDependencies: []string{"jest", "@types/jest"},
			ConfigFiles:          []string{"jest.config.js", "jest.config.ts"},
			NegativeCheckAgainst: []string{"Vitest", "Mocha"},
		},
		{
			ID:                   "vitest",
			Name:                 "Vitest",
			Category:             engine.CategoryTesting,
			ManifestDependencies: []string{"vitest"},
			ConfigFiles:          []string{"vitest.config.ts", "vitest.config.js"},
			NegativeCheckAgainst: []string{"Jest"},
		},
		{
			ID:                   "playwright",
			Name:                 "Playwright",
			Category:             engine.CategoryTesting,
			ManifestDependencies: []string{"@playwright/test"},
			ConfigFiles:          []string{"playwright.config.ts", "playwright.config.js"},
			NegativeCheckAgainst: []string{"Cypress"},
		},
		{
			ID:                   "cypress",
			Name:                 "Cypress",
			Category:             engine.CategoryTesting,
			ManifestDependencies: []string{"cypress"},
			ConfigFiles:          []string{"cypress.config.ts", "cypress.config.js"},
			NegativeCheckAgainst: []string{"Playwright"},
		},
		{
			ID:                   "pytest",
			Name:                 "pytest",
			Category:             engine.CategoryTesting,
			ManifestDependencies: []string{"pytest"},
			ConfigFiles:          []string{"pytest.ini", ".pytest.ini"},
			NegativeCheckAgainst: []string{"unittest"},
		},
	}
}

// DetectTechnologies evaluates all evidence against the inventory and extracted dependencies,
// calculating deterministic confidence levels and negative assertions.
func DetectTechnologies(
	inv *inventory.ScopeInventory,
	manifestDeps []DependencyRecord,
	envVarNames []string,
) []engine.TechnologyItem {
	catalog := RuleCatalog()

	// Build lookup indices for fast, deterministic evidence matching
	depIndex := make(map[string]DependencyRecord, len(manifestDeps))
	for _, dep := range manifestDeps {
		depIndex[strings.ToLower(dep.Name)] = dep
	}

	fileIndex := make(map[string]inventory.FileEntry, len(inv.Files))
	baseIndex := make(map[string][]inventory.FileEntry)
	for _, file := range inv.Files {
		if file.IsExcluded {
			continue
		}
		norm := strings.ToLower(file.RelPath)
		fileIndex[norm] = file
		base := strings.ToLower(filepath.Base(norm))
		baseIndex[base] = append(baseIndex[base], file)
	}

	envIndex := make(map[string]struct{}, len(envVarNames))
	for _, name := range envVarNames {
		envIndex[strings.ToUpper(name)] = struct{}{}
	}

	// 1. Detect Languages directly from ScopeInventory
	results := make([]engine.TechnologyItem, 0, len(catalog)+len(inv.LanguagesFound))
	detectedTechNames := make(map[string]bool)

	for _, lang := range inv.LanguagesFound {
		if lang == "Markdown" || lang == "Config" {
			continue
		}
		item := engine.TechnologyItem{
			ID:         strings.ToLower(lang),
			Name:       lang,
			Category:   engine.CategoryLanguage,
			Confidence: engine.ConfidenceConfirmed,
			Evidence: []engine.EvidenceItem{
				{
					FilePath: "inventory",
					RuleType: engine.RuleFilePresence,
					Detail:   "Identified primary source language files",
				},
			},
			CanonicalEvidence: []engine.Evidence{
				{
					ID:          "tech_lang_" + strings.ToLower(lang),
					SourceType:  "inventory",
					FilePath:    "inventory",
					Detector:    "technologies_parser",
					Confidence:  engine.EpistemicConfidenceConfirmed,
					Description: "Identified primary source language files",
				},
			},
			NegativeAssertionsPassed: make([]string, 0),
		}
		results = append(results, item)
		detectedTechNames[lang] = true
	}

	// 2. Evaluate Technology Catalog Rules
	for _, matcher := range catalog {
		evidence := make([]engine.EvidenceItem, 0)
		canonicalEvidence := make([]engine.Evidence, 0)
		var version *string

		// Check Manifest Dependencies
		for _, reqDep := range matcher.ManifestDependencies {
			if match, found := depIndex[strings.ToLower(reqDep)]; found {
				detail := "Declared dependency in manifest"
				if match.Version != "" {
					detail = "Declared version: " + match.Version
					v := match.Version
					version = &v
				}
				evidence = append(evidence, engine.EvidenceItem{
					FilePath: match.SourcePath,
					RuleType: engine.RuleManifestDep,
					Detail:   detail,
				})
				canonicalEvidence = append(canonicalEvidence, engine.Evidence{
					ID:          "tech_dep_" + matcher.ID,
					SourceType:  "manifest",
					FilePath:    match.SourcePath,
					Detector:    "technologies_parser",
					Confidence:  engine.EpistemicConfidenceConfirmed,
					Description: detail,
				})
			}
		}

		// Check Config Files
		for _, cfgFile := range matcher.ConfigFiles {
			lowerCfg := strings.ToLower(cfgFile)
			if files, found := baseIndex[lowerCfg]; found && len(files) > 0 {
				for _, f := range files {
					evidence = append(evidence, engine.EvidenceItem{
						FilePath: f.RelPath,
						RuleType: engine.RuleConfigFile,
						Detail:   "Configuration file presence",
					})
					canonicalEvidence = append(canonicalEvidence, engine.Evidence{
						ID:          "tech_cfg_" + matcher.ID,
						SourceType:  "config",
						FilePath:    f.RelPath,
						Detector:    "technologies_parser",
						Confidence:  engine.EpistemicConfidenceStronglyInferred,
						Description: "Configuration file presence",
					})
				}
			}
		}

		// Check File Globs
		for _, glob := range matcher.FileGlobs {
			lowerGlob := strings.ToLower(glob)
			for path := range fileIndex {
				if matchGlobSuffix(path, lowerGlob) {
					evidence = append(evidence, engine.EvidenceItem{
						FilePath: path,
						RuleType: engine.RuleFilePresence,
						Detail:   "Architectural schema or workflow file presence",
					})
					canonicalEvidence = append(canonicalEvidence, engine.Evidence{
						ID:          "tech_glob_" + matcher.ID,
						SourceType:  "file",
						FilePath:    path,
						Detector:    "technologies_parser",
						Confidence:  engine.EpistemicConfidenceWeaklyInferred,
						Description: "Architectural schema or workflow file presence",
					})
				}
			}
		}

		// Check Environment Variables
		for _, envKey := range matcher.EnvKeywords {
			if _, found := envIndex[strings.ToUpper(envKey)]; found {
				evidence = append(evidence, engine.EvidenceItem{
					FilePath: ".env.example",
					RuleType: engine.RuleEnvVar,
					Detail:   "Environment configuration reference: " + envKey,
				})
				canonicalEvidence = append(canonicalEvidence, engine.Evidence{
					ID:          "tech_env_" + matcher.ID,
					SourceType:  "env",
					FilePath:    ".env.example",
					Detector:    "technologies_parser",
					Confidence:  engine.EpistemicConfidenceWeaklyInferred,
					Description: "Environment configuration reference: " + envKey,
				})
			}
		}

		// Calculate Confidence
		var confidence engine.ConfidenceLevel
		if len(evidence) >= 2 {
			confidence = engine.ConfidenceConfirmed
		} else if len(evidence) == 1 {
			// Single manifest dependency or distinct config is Confirmed for top tools
			if evidence[0].RuleType == engine.RuleManifestDep || evidence[0].RuleType == engine.RuleConfigFile {
				confidence = engine.ConfidenceConfirmed
			} else {
				confidence = engine.ConfidenceProbable
			}
		} else {
			confidence = engine.ConfidenceNotDetected
		}

		if confidence != engine.ConfidenceNotDetected {
			detectedTechNames[matcher.Name] = true
			results = append(results, engine.TechnologyItem{
				ID:                       matcher.ID,
				Name:                     matcher.Name,
				Category:                 matcher.Category,
				Version:                  version,
				Confidence:               confidence,
				Evidence:                 evidence,
				CanonicalEvidence:        canonicalEvidence,
				NegativeAssertionsPassed: make([]string, 0),
			})
		}
	}

	// 3. Compute Negative Assertions for all detected technologies
	for i := range results {
		techName := results[i].Name
		for _, matcher := range catalog {
			if matcher.Name == techName {
				for _, comp := range matcher.NegativeCheckAgainst {
					if !detectedTechNames[comp] {
						results[i].NegativeAssertionsPassed = append(results[i].NegativeAssertionsPassed, comp)
					}
				}
				sort.Strings(results[i].NegativeAssertionsPassed)
			}
		}
	}

	// Sort technologies deterministically by category then name
	sort.Slice(results, func(i, j int) bool {
		if results[i].Category == results[j].Category {
			return results[i].Name < results[j].Name
		}
		return results[i].Category < results[j].Category
	})

	return results
}

func matchGlobSuffix(path string, glob string) bool {
	if strings.Contains(glob, "*") {
		parts := strings.Split(glob, "*")
		if len(parts) == 2 {
			return strings.HasPrefix(path, parts[0]) && strings.HasSuffix(path, parts[1])
		}
	}
	return path == glob
}
