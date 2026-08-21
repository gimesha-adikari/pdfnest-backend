package exclusion

import "strings"

// Engine manages and evaluates the 5-tier exclusion precedence hierarchy.
type Engine struct {
	mandatoryRules []Rule
	forceIncludes  []string
	customRules    []Rule
	gitignoreRules []Rule
	presetRules    []Rule
}

// Config provides initial rule sets to configure the exclusion engine.
type Config struct {
	CustomPatterns []string
	EnabledPresets []string
	ForceIncludes  []string
	GitignoreRules []string
}

// NewEngine instantiates a thread-safe, immutable exclusion engine.
func NewEngine(cfg Config) *Engine {
	e := &Engine{
		mandatoryRules: MandatorySecurityRules(),
		forceIncludes:  make([]string, 0, len(cfg.ForceIncludes)),
		customRules:    make([]Rule, 0, len(cfg.CustomPatterns)),
		gitignoreRules: make([]Rule, 0, len(cfg.GitignoreRules)),
		presetRules:    make([]Rule, 0, 10),
	}

	// 1. Force includes
	for _, fi := range cfg.ForceIncludes {
		cleaned := strings.TrimPrefix(strings.TrimSpace(fi), "!")
		norm := NormalizePath(cleaned)
		if norm != "" {
			e.forceIncludes = append(e.forceIncludes, norm)
		}
	}

	// 2. Custom patterns
	for idx, pat := range cfg.CustomPatterns {
		norm := NormalizePath(pat)
		if norm != "" {
			e.customRules = append(e.customRules, Rule{
				ID:          "custom-" + norm,
				Pattern:     pat,
				Precedence:  PrecedenceCustom,
				Reason:      "User Custom Exclusion Rule",
				Category:    "Custom",
				Enabled:     true,
				IsMandatory: false,
			})
			_ = idx
		}
	}

	// 3. Gitignore rules
	for _, g := range cfg.GitignoreRules {
		norm := NormalizePath(g)
		if norm != "" {
			e.gitignoreRules = append(e.gitignoreRules, Rule{
				ID:          "gitignore-" + norm,
				Pattern:     g,
				Precedence:  PrecedenceGitignore,
				Reason:      "Repository .gitignore Rule",
				Category:    "VCS",
				Enabled:     true,
				IsMandatory: false,
			})
		}
	}

	// 4. Default presets (filtered by enabled preset IDs or active by default)
	allPresets := DefaultPresets()
	enabledSet := make(map[string]bool, len(cfg.EnabledPresets))
	for _, ep := range cfg.EnabledPresets {
		enabledSet[ep] = true
	}

	for _, pr := range allPresets {
		// If explicit enabled list is provided, filter; otherwise enable defaults
		if len(cfg.EnabledPresets) > 0 {
			if enabledSet[pr.ID] || enabledSet[pr.Pattern] {
				pr.Enabled = true
				e.presetRules = append(e.presetRules, pr)
			}
		} else if pr.Enabled {
			e.presetRules = append(e.presetRules, pr)
		}
	}

	return e
}

// Evaluate determines whether a file path is included or excluded according to the strict 5-tier precedence.
//
// Invariants enforced:
//  1. MANDATORY_SECURITY ALWAYS WINS. Force-include CANNOT override security exclusions.
//  2. FORCE_INCLUDE overrides Custom, Gitignore, and Preset exclusions.
//  3. CUSTOM overrides Gitignore and Preset exclusions.
//  4. GITIGNORE overrides Preset exclusions.
//  5. PRESET exclusions apply if no higher tier matched.
func (e *Engine) Evaluate(relPath string) EvaluationResult {
	normPath := NormalizePath(relPath)

	// Step 1: Check Mandatory Security Rules (Tier 1) - Highest Priority
	if IsSecretEnvFile(normPath) {
		return EvaluationResult{
			IsExcluded:     true,
			MatchedPattern: "**/.env*",
			Precedence:     PrecedenceMandatorySecurity,
			Reason:         "Production Environment Secret File",
			IsMandatory:    true,
			Overridable:    false,
		}
	}

	for _, rule := range e.mandatoryRules {
		if rule.Enabled && (MatchGlob(rule.Pattern, normPath) || MatchGlob(strings.ToLower(rule.Pattern), strings.ToLower(normPath))) {
			return EvaluationResult{
				IsExcluded:     true,
				MatchedPattern: rule.Pattern,
				Precedence:     PrecedenceMandatorySecurity,
				Reason:         rule.Reason,
				IsMandatory:    true,
				Overridable:    false,
			}
		}
	}

	// Step 2: Check Force Includes (Tier 2)
	for _, fi := range e.forceIncludes {
		if MatchGlob(fi, normPath) {
			return EvaluationResult{
				IsExcluded:     false,
				MatchedPattern: "!" + fi,
				Precedence:     PrecedenceForceInclude,
				Reason:         "User Force-Include Override",
				IsMandatory:    false,
				Overridable:    true,
			}
		}
	}

	// Step 3: Check User Custom Exclusions (Tier 3)
	for _, rule := range e.customRules {
		if rule.Enabled && MatchGlob(rule.Pattern, normPath) {
			return EvaluationResult{
				IsExcluded:     true,
				MatchedPattern: rule.Pattern,
				Precedence:     PrecedenceCustom,
				Reason:         rule.Reason,
				IsMandatory:    false,
				Overridable:    true,
			}
		}
	}

	// Step 4: Check Repository .gitignore Rules (Tier 4)
	for _, rule := range e.gitignoreRules {
		if rule.Enabled && MatchGlob(rule.Pattern, normPath) {
			return EvaluationResult{
				IsExcluded:     true,
				MatchedPattern: rule.Pattern,
				Precedence:     PrecedenceGitignore,
				Reason:         rule.Reason,
				IsMandatory:    false,
				Overridable:    true,
			}
		}
	}

	// Step 5: Check Default System Presets (Tier 5)
	for _, rule := range e.presetRules {
		if rule.Enabled && MatchGlob(rule.Pattern, normPath) {
			return EvaluationResult{
				IsExcluded:     true,
				MatchedPattern: rule.Pattern,
				Precedence:     PrecedencePreset,
				Reason:         rule.Reason,
				IsMandatory:    false,
				Overridable:    true,
			}
		}
	}

	// Default: Not excluded
	return EvaluationResult{
		IsExcluded:     false,
		MatchedPattern: "",
		Precedence:     0,
		Reason:         "Standard Included File",
		IsMandatory:    false,
		Overridable:    true,
	}
}
