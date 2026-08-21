package parsers

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"pdfnest-backend/internal/analyzer/engine"
	"pdfnest-backend/internal/analyzer/engine/exclusion"
	"pdfnest-backend/internal/analyzer/engine/inventory"
)

// EnvironmentScanner extracts environment variables from safe templates and source code references.
type EnvironmentScanner struct{}

// NewEnvironmentScanner creates a new environment scanner.
func NewEnvironmentScanner() *EnvironmentScanner {
	return &EnvironmentScanner{}
}

// ScanEnvironmentVariables parses safe template files and inspects included source code files for variable references.
func (s *EnvironmentScanner) ScanEnvironmentVariables(
	rootDir string,
	inv *inventory.ScopeInventory,
) []engine.EnvironmentVariable {
	varMap := make(map[string]*engine.EnvironmentVariable)

	// 1. Scan safe environment templates (.env.example, .env.sample, .env.template)
	for _, file := range inv.Files {
		if file.IsExcluded {
			continue
		}
		if exclusion.IsSafeEnvTemplate(file.RelPath) {
			absPath := filepath.Join(rootDir, file.RelPath)
			parseEnvTemplateFile(absPath, file.RelPath, varMap)
		}
	}

	// 2. Scan included source files for code references
	reJsEnv := regexp.MustCompile(`process\.env\.([A-Z0-9_]+)`)
	reGoEnv := regexp.MustCompile(`os\.(?:Getenv|LookupEnv)\("([A-Z0-9_]+)"\)`)
	rePyEnv := regexp.MustCompile(`os\.(?:environ\["([A-Z0-9_]+)"\]|environ\.get\("([A-Z0-9_]+)"\)|getenv\("([A-Z0-9_]+)"\))`)

	for _, file := range inv.Files {
		if file.IsExcluded || file.Category != inventory.CategorySource {
			continue
		}
		// Skip large files (> 512 KB) during static regex scanning
		if file.Size > 512*1024 {
			continue
		}

		absPath := filepath.Join(rootDir, file.RelPath)
		content, err := os.ReadFile(absPath)
		if err != nil {
			continue
		}

		lines := strings.Split(string(content), "\n")
		for lineIdx, line := range lines {
			lineNum := lineIdx + 1

			// JS / TS
			for _, m := range reJsEnv.FindAllStringSubmatch(line, -1) {
				if len(m) == 2 {
					recordEnvRef(m[1], file.RelPath, lineNum, varMap)
				}
			}

			// Go
			for _, m := range reGoEnv.FindAllStringSubmatch(line, -1) {
				if len(m) == 2 {
					recordEnvRef(m[1], file.RelPath, lineNum, varMap)
				}
			}

			// Python
			for _, m := range rePyEnv.FindAllStringSubmatch(line, -1) {
				for i := 1; i < len(m); i++ {
					if m[i] != "" {
						recordEnvRef(m[i], file.RelPath, lineNum, varMap)
					}
				}
			}
		}
	}

	results := make([]engine.EnvironmentVariable, 0, len(varMap))
	for _, v := range varMap {
		sort.Strings(v.References)
		results = append(results, *v)
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Name < results[j].Name
	})

	return results
}

func parseEnvTemplateFile(absPath string, relPath string, varMap map[string]*engine.EnvironmentVariable) {
	content, err := os.ReadFile(absPath)
	if err != nil {
		return
	}

	scanner := bufio.NewScanner(bytes.NewReader(content))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) >= 1 {
			name := strings.TrimSpace(parts[0])
			if name == "" {
				continue
			}

			var val *string
			if len(parts) == 2 {
				rawVal := strings.TrimSpace(parts[1])
				rawVal = strings.Trim(rawVal, "\"'")
				// Mask potential secret defaults
				inferred := inferEnvType(name, rawVal)
				if inferred == engine.EnvVarSecret {
					rawVal = ""
				}
				if rawVal != "" {
					val = &rawVal
				}
			}

			inferredType := inferEnvType(name, "")
			if val != nil {
				inferredType = inferEnvType(name, *val)
			}

			if existing, exists := varMap[name]; exists {
				if existing.DefaultValue == nil && val != nil {
					existing.DefaultValue = val
				}
			} else {
				varMap[name] = &engine.EnvironmentVariable{
					Name:         name,
					Required:     val == nil || *val == "",
					DefaultValue: val,
					InferredType: inferredType,
					Source:       relPath,
					References:   []string{relPath},
				}
			}
		}
	}
}

func recordEnvRef(name string, relPath string, lineNum int, varMap map[string]*engine.EnvironmentVariable) {
	ref := fmt.Sprintf("%s:%d", relPath, lineNum)
	if existing, exists := varMap[name]; exists {
		existing.References = append(existing.References, ref)
	} else {
		varMap[name] = &engine.EnvironmentVariable{
			Name:         name,
			Required:     true,
			InferredType: inferEnvType(name, ""),
			Source:       relPath,
			References:   []string{ref},
		}
	}
}

func inferEnvType(name string, sampleValue string) engine.EnvVarType {
	upper := strings.ToUpper(name)
	if strings.Contains(upper, "SECRET") ||
		strings.Contains(upper, "PASSWORD") ||
		strings.Contains(upper, "TOKEN") ||
		strings.Contains(upper, "KEY") ||
		strings.Contains(upper, "AUTH") ||
		strings.Contains(upper, "PRIVATE") {
		return engine.EnvVarSecret
	}

	if strings.Contains(upper, "URL") ||
		strings.Contains(upper, "URI") ||
		strings.HasPrefix(sampleValue, "http://") ||
		strings.HasPrefix(sampleValue, "https://") ||
		strings.HasPrefix(sampleValue, "redis://") ||
		strings.HasPrefix(sampleValue, "postgres://") {
		return engine.EnvVarURL
	}

	if strings.Contains(upper, "PORT") ||
		strings.Contains(upper, "TIMEOUT") ||
		strings.Contains(upper, "COUNT") ||
		strings.Contains(upper, "LIMIT") {
		return engine.EnvVarNumber
	}

	if strings.Contains(upper, "ENABLE") ||
		strings.Contains(upper, "DISABLE") ||
		strings.Contains(upper, "IS_") ||
		strings.Contains(upper, "DEBUG") {
		return engine.EnvVarBoolean
	}

	return engine.EnvVarString
}
