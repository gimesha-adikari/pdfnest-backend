package exclusion

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExclusionPrecedenceHierarchy(t *testing.T) {
	engine := NewEngine(Config{
		CustomPatterns: []string{"legacy/**", "docs/drafts/**"},
		GitignoreRules: []string{"temp/**", "*.log"},
		EnabledPresets: []string{"preset-node-modules", "preset-build-dist"},
		ForceIncludes:  []string{"!legacy/keeper.ts", "!dist/important.js", "!.env", "!id_rsa", "!certs/prod.pem"},
	})

	t.Run("Mandatory Security ALWAYS beats Force-Include", func(t *testing.T) {
		// Even though !.env and !id_rsa and !certs/prod.pem were force-included, security MUST win!
		evalEnv := engine.Evaluate(".env")
		assert.True(t, evalEnv.IsExcluded, ".env must be excluded despite force-include")
		assert.Equal(t, PrecedenceMandatorySecurity, evalEnv.Precedence)
		assert.False(t, evalEnv.Overridable)

		evalEnvLocal := engine.Evaluate(".env.local")
		assert.True(t, evalEnvLocal.IsExcluded)
		assert.Equal(t, PrecedenceMandatorySecurity, evalEnvLocal.Precedence)

		evalEnvProd := engine.Evaluate(".env.production")
		assert.True(t, evalEnvProd.IsExcluded)
		assert.Equal(t, PrecedenceMandatorySecurity, evalEnvProd.Precedence)

		evalRsa := engine.Evaluate("id_rsa")
		assert.True(t, evalRsa.IsExcluded, "id_rsa must be excluded despite force-include")
		assert.Equal(t, PrecedenceMandatorySecurity, evalRsa.Precedence)

		evalPem := engine.Evaluate("certs/prod.pem")
		assert.True(t, evalPem.IsExcluded, "*.pem must be excluded despite force-include")
		assert.Equal(t, PrecedenceMandatorySecurity, evalPem.Precedence)

		evalKey := engine.Evaluate("server.key")
		assert.True(t, evalKey.IsExcluded, "*.key must be excluded")
		assert.Equal(t, PrecedenceMandatorySecurity, evalKey.Precedence)

		evalCreds := engine.Evaluate("config/credentials.json")
		assert.True(t, evalCreds.IsExcluded, "credentials.json must be excluded")
		assert.Equal(t, PrecedenceMandatorySecurity, evalCreds.Precedence)
	})

	t.Run("Mandatory Security Case-Insensitive Hardening (8A-3)", func(t *testing.T) {
		// Uppercase / Mixed-case security variants MUST still be excluded by Tier 1!
		assert.True(t, engine.Evaluate(".ENV").IsExcluded)
		assert.True(t, engine.Evaluate(".ENV.LOCAL").IsExcluded)
		assert.True(t, engine.Evaluate(".ENV.PRODUCTION").IsExcluded)
		assert.True(t, engine.Evaluate("ID_RSA").IsExcluded)
		assert.True(t, engine.Evaluate("ID_RSA_BACKUP").IsExcluded)
		assert.True(t, engine.Evaluate("ID_DSA").IsExcluded)
		assert.True(t, engine.Evaluate("ID_ED25519").IsExcluded)
		assert.True(t, engine.Evaluate("SERVER.KEY").IsExcluded)
		assert.True(t, engine.Evaluate("certs/SERVER.PEM").IsExcluded)
		assert.True(t, engine.Evaluate("config/CREDENTIALS.JSON").IsExcluded)
		assert.True(t, engine.Evaluate("Config/Credentials.Json").IsExcluded)

		// All must have PrecedenceMandatorySecurity
		assert.Equal(t, PrecedenceMandatorySecurity, engine.Evaluate(".ENV.PRODUCTION").Precedence)
		assert.Equal(t, PrecedenceMandatorySecurity, engine.Evaluate("ID_RSA").Precedence)
		assert.Equal(t, PrecedenceMandatorySecurity, engine.Evaluate("certs/SERVER.PEM").Precedence)
	})

	t.Run("Safe .env.example is NOT excluded as a secret", func(t *testing.T) {
		evalExample := engine.Evaluate(".env.example")
		assert.False(t, evalExample.IsExcluded, ".env.example is a safe public template and must not be excluded as a secret")

		evalSample := engine.Evaluate(".env.sample")
		assert.False(t, evalSample.IsExcluded)

		evalTemplate := engine.Evaluate(".env.template")
		assert.False(t, evalTemplate.IsExcluded)
	})

	t.Run("Force-Include beats Custom Exclusion", func(t *testing.T) {
		// legacy/** is custom-excluded, but !legacy/keeper.ts is force-included
		evalKeeper := engine.Evaluate("legacy/keeper.ts")
		assert.False(t, evalKeeper.IsExcluded, "legacy/keeper.ts should be included via force-include")
		assert.Equal(t, PrecedenceForceInclude, evalKeeper.Precedence)

		// legacy/other.ts is NOT force-included and must be excluded
		evalOther := engine.Evaluate("legacy/other.ts")
		assert.True(t, evalOther.IsExcluded)
		assert.Equal(t, PrecedenceCustom, evalOther.Precedence)
	})

	t.Run("Force-Include beats Preset Exclusion", func(t *testing.T) {
		// dist/** is preset-excluded, but !dist/important.js is force-included
		evalImportant := engine.Evaluate("dist/important.js")
		assert.False(t, evalImportant.IsExcluded, "dist/important.js should be included via force-include")
		assert.Equal(t, PrecedenceForceInclude, evalImportant.Precedence)

		// dist/bundle.js is NOT force-included and must be excluded
		evalBundle := engine.Evaluate("dist/bundle.js")
		assert.True(t, evalBundle.IsExcluded)
		assert.Equal(t, PrecedencePreset, evalBundle.Precedence)
	})

	t.Run("Custom Exclusion beats Gitignore", func(t *testing.T) {
		evalDraft := engine.Evaluate("docs/drafts/post.md")
		assert.True(t, evalDraft.IsExcluded)
		assert.Equal(t, PrecedenceCustom, evalDraft.Precedence)
	})

	t.Run("Gitignore beats Preset", func(t *testing.T) {
		evalLog := engine.Evaluate("server.log")
		assert.True(t, evalLog.IsExcluded)
		assert.Equal(t, PrecedenceGitignore, evalLog.Precedence)
	})

	t.Run("Preset applies when no higher tier matches", func(t *testing.T) {
		evalNodeModules := engine.Evaluate("node_modules/express/index.js")
		assert.True(t, evalNodeModules.IsExcluded)
		assert.Equal(t, PrecedencePreset, evalNodeModules.Precedence)
	})

	t.Run("Standard file is included when no rule matches", func(t *testing.T) {
		evalSource := engine.Evaluate("src/main.ts")
		assert.False(t, evalSource.IsExcluded)
		assert.Equal(t, PrecedenceLevel(0), evalSource.Precedence)
	})
}
