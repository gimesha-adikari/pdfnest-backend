package exclusion

// PrecedenceLevel represents the priority tier in the 5-tier exclusion hierarchy.
type PrecedenceLevel int

const (
	// PrecedenceMandatorySecurity is highest priority (1) and CANNOT be overridden by force-includes.
	PrecedenceMandatorySecurity PrecedenceLevel = 1

	// PrecedenceForceInclude is priority (2), overriding custom, gitignore, and preset exclusions.
	PrecedenceForceInclude PrecedenceLevel = 2

	// PrecedenceCustom represents developer-supplied custom glob exclusions (3).
	PrecedenceCustom PrecedenceLevel = 3

	// PrecedenceGitignore represents patterns parsed from repository .gitignore files (4).
	PrecedenceGitignore PrecedenceLevel = 4

	// PrecedencePreset represents standard default system presets (5, e.g. node_modules, build outputs).
	PrecedencePreset PrecedenceLevel = 5
)

func (p PrecedenceLevel) String() string {
	switch p {
	case PrecedenceMandatorySecurity:
		return "MANDATORY_SECURITY"
	case PrecedenceForceInclude:
		return "FORCE_INCLUDE"
	case PrecedenceCustom:
		return "CUSTOM"
	case PrecedenceGitignore:
		return "GITIGNORE"
	case PrecedencePreset:
		return "PRESET"
	default:
		return "UNKNOWN"
	}
}

// Rule defines a single exclusion or force-include pattern.
type Rule struct {
	ID          string          `json:"id"`
	Pattern     string          `json:"pattern"`
	Precedence  PrecedenceLevel `json:"precedence"`
	Reason      string          `json:"reason"`
	Category    string          `json:"category"`
	Enabled     bool            `json:"enabled"`
	IsMandatory bool            `json:"isMandatory"`
}

// EvaluationResult details the exact match outcome and provenance for a scanned file.
type EvaluationResult struct {
	IsExcluded     bool            `json:"isExcluded"`
	MatchedPattern string          `json:"matchedPattern"`
	Precedence     PrecedenceLevel `json:"precedence"`
	Reason         string          `json:"reason"`
	IsMandatory    bool            `json:"isMandatory"`
	Overridable    bool            `json:"overridable"`
}
