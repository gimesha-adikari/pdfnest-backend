package exclusion

import (
	"path/filepath"
	"strings"
)

// MandatorySecurityRules returns the non-overridable security exclusion rules.
// These rules protect against credential exposure, VCS internal state leaks, and binary payloads.
func MandatorySecurityRules() []Rule {
	return []Rule{
		{
			ID:          "sec-cert-pem",
			Pattern:     "**/*.pem",
			Precedence:  PrecedenceMandatorySecurity,
			Reason:      "Private TLS/SSL Certificate",
			Category:    "Security",
			Enabled:     true,
			IsMandatory: true,
		},
		{
			ID:          "sec-key",
			Pattern:     "**/*.key",
			Precedence:  PrecedenceMandatorySecurity,
			Reason:      "Private Cryptographic Key",
			Category:    "Security",
			Enabled:     true,
			IsMandatory: true,
		},
		{
			ID:          "sec-ssh-rsa",
			Pattern:     "**/id_rsa*",
			Precedence:  PrecedenceMandatorySecurity,
			Reason:      "SSH Private Key",
			Category:    "Security",
			Enabled:     true,
			IsMandatory: true,
		},
		{
			ID:          "sec-ssh-dsa",
			Pattern:     "**/id_dsa*",
			Precedence:  PrecedenceMandatorySecurity,
			Reason:      "SSH DSA Private Key",
			Category:    "Security",
			Enabled:     true,
			IsMandatory: true,
		},
		{
			ID:          "sec-ssh-ed25519",
			Pattern:     "**/id_ed25519*",
			Precedence:  PrecedenceMandatorySecurity,
			Reason:      "SSH Ed25519 Private Key",
			Category:    "Security",
			Enabled:     true,
			IsMandatory: true,
		},
		{
			ID:          "sec-credentials-json",
			Pattern:     "**/credentials.json",
			Precedence:  PrecedenceMandatorySecurity,
			Reason:      "Service Account Credentials",
			Category:    "Security",
			Enabled:     true,
			IsMandatory: true,
		},
		{
			ID:          "sec-vcs-git",
			Pattern:     "**/.git/**",
			Precedence:  PrecedenceMandatorySecurity,
			Reason:      "Git Internal Version Control Metadata",
			Category:    "VCS",
			Enabled:     true,
			IsMandatory: true,
		},
		{
			ID:          "sec-vcs-svn",
			Pattern:     "**/.svn/**",
			Precedence:  PrecedenceMandatorySecurity,
			Reason:      "Subversion Metadata",
			Category:    "VCS",
			Enabled:     true,
			IsMandatory: true,
		},
		{
			ID:          "sec-vcs-hg",
			Pattern:     "**/.hg/**",
			Precedence:  PrecedenceMandatorySecurity,
			Reason:      "Mercurial Metadata",
			Category:    "VCS",
			Enabled:     true,
			IsMandatory: true,
		},
		{
			ID:          "sec-bin-exe",
			Pattern:     "**/*.exe",
			Precedence:  PrecedenceMandatorySecurity,
			Reason:      "Windows Binary Executable",
			Category:    "Binary",
			Enabled:     true,
			IsMandatory: true,
		},
		{
			ID:          "sec-bin-dll",
			Pattern:     "**/*.dll",
			Precedence:  PrecedenceMandatorySecurity,
			Reason:      "Dynamic Link Library Binary",
			Category:    "Binary",
			Enabled:     true,
			IsMandatory: true,
		},
		{
			ID:          "sec-bin-so",
			Pattern:     "**/*.so",
			Precedence:  PrecedenceMandatorySecurity,
			Reason:      "Shared Object Library Binary",
			Category:    "Binary",
			Enabled:     true,
			IsMandatory: true,
		},
		{
			ID:          "sec-bin-dylib",
			Pattern:     "**/*.dylib",
			Precedence:  PrecedenceMandatorySecurity,
			Reason:      "Mach-O Dynamic Library",
			Category:    "Binary",
			Enabled:     true,
			IsMandatory: true,
		},
	}
}

// IsSafeEnvTemplate checks if a filename is a safe public configuration template (e.g. .env.example).
func IsSafeEnvTemplate(relPath string) bool {
	base := strings.ToLower(filepath.Base(relPath))
	safeSuffixes := []string{
		".env.example",
		".env.sample",
		".env.template",
		".env.dist",
		".env.defaults",
		"env.example",
		"env.sample",
		"env.template",
	}
	for _, s := range safeSuffixes {
		if base == s || strings.HasSuffix(base, s) {
			return true
		}
	}
	return false
}

// IsSecretEnvFile checks if a file is an active environment secret file (e.g. .env, .env.local, .env.production).
func IsSecretEnvFile(relPath string) bool {
	base := strings.ToLower(filepath.Base(relPath))
	if IsSafeEnvTemplate(relPath) {
		return false
	}
	if base == ".env" || strings.HasPrefix(base, ".env.") {
		return true
	}
	return false
}

// DefaultPresets returns the standard configurable preset rules for build outputs and dependencies.
func DefaultPresets() []Rule {
	return []Rule{
		{
			ID:          "preset-node-modules",
			Pattern:     "**/node_modules/**",
			Precedence:  PrecedencePreset,
			Reason:      "Node.js Dependency Tree",
			Category:    "Dependencies",
			Enabled:     true,
			IsMandatory: false,
		},
		{
			ID:          "preset-vendor",
			Pattern:     "**/vendor/**",
			Precedence:  PrecedencePreset,
			Reason:      "Third-Party Vendor Directory",
			Category:    "Dependencies",
			Enabled:     true,
			IsMandatory: false,
		},
		{
			ID:          "preset-python-venv",
			Pattern:     "**/.venv/**",
			Precedence:  PrecedencePreset,
			Reason:      "Python Virtual Environment",
			Category:    "Dependencies",
			Enabled:     true,
			IsMandatory: false,
		},
		{
			ID:          "preset-python-venv2",
			Pattern:     "**/venv/**",
			Precedence:  PrecedencePreset,
			Reason:      "Python Virtual Environment",
			Category:    "Dependencies",
			Enabled:     true,
			IsMandatory: false,
		},
		{
			ID:          "preset-rust-target",
			Pattern:     "**/target/**",
			Precedence:  PrecedencePreset,
			Reason:      "Rust / Java Build Target Directory",
			Category:    "Build",
			Enabled:     true,
			IsMandatory: false,
		},
		{
			ID:          "preset-build-dist",
			Pattern:     "**/dist/**",
			Precedence:  PrecedencePreset,
			Reason:      "Compiled Distribution Build Artifacts",
			Category:    "Build",
			Enabled:     true,
			IsMandatory: false,
		},
		{
			ID:          "preset-build-out",
			Pattern:     "**/build/**",
			Precedence:  PrecedencePreset,
			Reason:      "Build Output Directory",
			Category:    "Build",
			Enabled:     true,
			IsMandatory: false,
		},
		{
			ID:          "preset-nextjs-cache",
			Pattern:     "**/.next/**",
			Precedence:  PrecedencePreset,
			Reason:      "Next.js Build Output and Cache",
			Category:    "Build",
			Enabled:     true,
			IsMandatory: false,
		},
		{
			ID:          "preset-coverage",
			Pattern:     "**/coverage/**",
			Precedence:  PrecedencePreset,
			Reason:      "Test Coverage Reports",
			Category:    "Build",
			Enabled:     true,
			IsMandatory: false,
		},
	}
}
