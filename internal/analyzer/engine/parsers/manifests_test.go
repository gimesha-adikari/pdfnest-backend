package parsers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParsePackageJSON(t *testing.T) {
	content := []byte(`{
		"name": "my-app",
		"version": "1.0.0",
		"scripts": {
			"dev": "next dev",
			"build": "next build"
		},
		"dependencies": {
			"next": "14.2.0",
			"react": "^18.3.0"
		},
		"devDependencies": {
			"typescript": "^5.4.0",
			"@types/react": "^18.3.0"
		}
	}`)

	parser := &PackageJSONParser{}
	res, err := parser.Parse("package.json", content)
	require.NoError(t, err)
	assert.Equal(t, "my-app", res.ProjectName)
	assert.Equal(t, "1.0.0", res.Version)
	assert.Equal(t, 2, len(res.RuntimeDeps))
	assert.Equal(t, 2, len(res.DevDeps))
	assert.Equal(t, "next", res.RuntimeDeps[0].Name)
	assert.Equal(t, "14.2.0", res.RuntimeDeps[0].Version)
	assert.Equal(t, "next dev", res.Scripts["dev"])
}

func TestParseMalformedPackageJSON(t *testing.T) {
	content := []byte(`{ "name": "bad-json", `)
	parser := &PackageJSONParser{}
	res, err := parser.Parse("package.json", content)
	require.NoError(t, err)
	assert.NotEmpty(t, res.Warnings)
}

func TestParseGoMod(t *testing.T) {
	content := []byte(`module github.com/user/service

go 1.23

require (
	github.com/gofiber/fiber/v2 v2.52.0
	gorm.io/gorm v1.25.7
	github.com/google/uuid v1.6.0 // indirect
)
`)

	parser := &GoModParser{}
	res, err := parser.Parse("go.mod", content)
	require.NoError(t, err)
	assert.Equal(t, "github.com/user/service", res.ProjectName)
	assert.Equal(t, "1.23", res.Version)
	assert.Equal(t, 2, len(res.RuntimeDeps))
	assert.Equal(t, 1, len(res.DevDeps))
	assert.Equal(t, "github.com/gofiber/fiber/v2", res.RuntimeDeps[0].Name)
	assert.Equal(t, "github.com/google/uuid", res.DevDeps[0].Name)
}

func TestParseCargoTOML(t *testing.T) {
	content := []byte(`[package]
name = "rust-service"
version = "0.1.0"

[dependencies]
tokio = "1.38.0"
serde = { version = "1.0.203", features = ["derive"] }

[dev-dependencies]
criterion = "0.5.1"
`)

	parser := &CargoTOMLParser{}
	res, err := parser.Parse("Cargo.toml", content)
	require.NoError(t, err)
	assert.Equal(t, "rust-service", res.ProjectName)
	assert.Equal(t, "0.1.0", res.Version)
	assert.Equal(t, 2, len(res.RuntimeDeps))
	assert.Equal(t, 1, len(res.DevDeps))
	assert.Equal(t, "serde", res.RuntimeDeps[0].Name)
	assert.Equal(t, "1.0.203", res.RuntimeDeps[0].Version)
}

func TestParseRequirementsTxt(t *testing.T) {
	content := []byte(`
# Production packages
fastapi==0.110.0
uvicorn[standard]>=0.28.0
pydantic~=2.6.4
`)

	parser := &RequirementsTxtParser{}
	res, err := parser.Parse("requirements.txt", content)
	require.NoError(t, err)
	assert.Equal(t, 3, len(res.RuntimeDeps))
	assert.Equal(t, "fastapi", res.RuntimeDeps[0].Name)
	assert.Equal(t, "==0.110.0", res.RuntimeDeps[0].Version)
}

func TestParsePyprojectTOML(t *testing.T) {
	content := []byte(`[tool.poetry]
name = "poetry-app"
version = "2.1.0"

[tool.poetry.dependencies]
python = "^3.11"
flask = "^3.0.0"

[tool.poetry.group.dev.dependencies]
pytest = "^8.0.0"
`)

	parser := &PyprojectTOMLParser{}
	res, err := parser.Parse("pyproject.toml", content)
	require.NoError(t, err)
	assert.Equal(t, "poetry-app", res.ProjectName)
	assert.Equal(t, 1, len(res.RuntimeDeps))
	assert.Equal(t, "flask", res.RuntimeDeps[0].Name)
	assert.Equal(t, 1, len(res.DevDeps))
	assert.Equal(t, "pytest", res.DevDeps[0].Name)
}

func TestParsePomXML(t *testing.T) {
	content := []byte(`<project>
	<groupId>com.example</groupId>
	<artifactId>demo-service</artifactId>
	<version>0.0.1-SNAPSHOT</version>
	<dependencies>
		<dependency>
			<groupId>org.springframework.boot</groupId>
			<artifactId>spring-boot-starter-web</artifactId>
			<version>3.2.3</version>
		</dependency>
		<dependency>
			<groupId>org.junit.jupiter</groupId>
			<artifactId>junit-jupiter</artifactId>
			<version>5.10.2</version>
			<scope>test</scope>
		</dependency>
	</dependencies>
</project>`)

	parser := &PomXMLParser{}
	res, err := parser.Parse("pom.xml", content)
	require.NoError(t, err)
	assert.Equal(t, "com.example:demo-service", res.ProjectName)
	assert.Equal(t, 1, len(res.RuntimeDeps))
	assert.Equal(t, "org.springframework.boot:spring-boot-starter-web", res.RuntimeDeps[0].Name)
	assert.Equal(t, 1, len(res.DevDeps))
}

func TestParseComposerJSON(t *testing.T) {
	content := []byte(`{
		"name": "laravel/laravel",
		"require": {
			"php": "^8.2",
			"laravel/framework": "^11.0"
		},
		"require-dev": {
			"phpunit/phpunit": "^11.0"
		}
	}`)

	parser := &ComposerJSONParser{}
	res, err := parser.Parse("composer.json", content)
	require.NoError(t, err)
	assert.Equal(t, "laravel/laravel", res.ProjectName)
	assert.Equal(t, 1, len(res.RuntimeDeps))
	assert.Equal(t, "laravel/framework", res.RuntimeDeps[0].Name)
	assert.Equal(t, 1, len(res.DevDeps))
}

func TestMergeDependenciesDeterminism(t *testing.T) {
	res1 := &ManifestResult{
		RuntimeDeps: []DependencyRecord{
			{Name: "express", Version: "4.19.0", Manager: "npm", SourcePath: "packages/api/package.json"},
			{Name: "zod", Version: "3.22.0", Manager: "npm", SourcePath: "packages/api/package.json"},
		},
	}
	res2 := &ManifestResult{
		RuntimeDeps: []DependencyRecord{
			{Name: "zod", Version: "3.23.0", Manager: "npm", SourcePath: "package.json"},
			{Name: "react", Version: "18.3.0", Manager: "npm", SourcePath: "packages/web/package.json"},
		},
	}

	runtime, _ := MergeDependencies([]*ManifestResult{res1, res2})
	assert.Equal(t, 3, len(runtime))
	assert.Equal(t, "express", runtime[0].Name)
	assert.Equal(t, "react", runtime[1].Name)
	assert.Equal(t, "zod", runtime[2].Name)
	// package.json precedes packages/api/package.json alphabetically
	assert.Equal(t, "3.23.0", runtime[2].Version)
}
