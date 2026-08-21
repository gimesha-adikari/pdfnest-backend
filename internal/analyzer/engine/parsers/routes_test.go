package parsers

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"pdfnest-backend/internal/analyzer/engine/inventory"
)

func TestExtractRoutes(t *testing.T) {
	tempDir := t.TempDir()

	// 1. Next.js App Router (app/api/users/[id]/route.ts)
	nextAppContent := `
export async function GET(req: Request) { return new Response("ok"); }
export async function POST(req: Request) { return new Response("created"); }
`
	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "app", "api", "users", "[id]"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "app", "api", "users", "[id]", "route.ts"), []byte(nextAppContent), 0644))

	// 2. Express (src/routes.ts)
	expressContent := `
app.get('/health', healthHandler);
router.post('/login', authMiddleware, loginHandler);
`
	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "src"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "src", "routes.ts"), []byte(expressContent), 0644))

	// 3. FastAPI (app/main.py)
	fastApiContent := `
@app.get("/items/{item_id}")
async def read_item(item_id: int):
    return {"item_id": item_id}
`
	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "app"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "app", "main.py"), []byte(fastApiContent), 0644))

	// 4. Fiber (backend/server.go)
	fiberContent := `
api.Get("/v1/tasks/:id", getTaskHandler)
`
	require.NoError(t, os.MkdirAll(filepath.Join(tempDir, "backend"), 0755))
	require.NoError(t, os.WriteFile(filepath.Join(tempDir, "backend", "server.go"), []byte(fiberContent), 0644))

	inv := &inventory.ScopeInventory{
		Files: []inventory.FileEntry{
			{RelPath: "app/api/users/[id]/route.ts", Category: inventory.CategorySource, IsExcluded: false},
			{RelPath: "src/routes.ts", Category: inventory.CategorySource, IsExcluded: false},
			{RelPath: "app/main.py", Category: inventory.CategorySource, IsExcluded: false},
			{RelPath: "backend/server.go", Category: inventory.CategorySource, IsExcluded: false},
		},
	}

	extractor := NewRouteExtractor()
	routes := extractor.ExtractRoutes(tempDir, inv)
	require.NotEmpty(t, routes)

	routeMap := make(map[string]bool)
	for _, r := range routes {
		key := r.Method + " " + r.Path
		routeMap[key] = true
	}

	// Verify Next.js routes
	assert.True(t, routeMap["GET /api/users/:id"])
	assert.True(t, routeMap["POST /api/users/:id"])

	// Verify Express routes
	assert.True(t, routeMap["GET /health"])
	assert.True(t, routeMap["POST /login"])

	// Verify FastAPI routes
	assert.True(t, routeMap["GET /items/{item_id}"])

	// Verify Fiber routes
	assert.True(t, routeMap["GET /v1/tasks/:id"])
}
