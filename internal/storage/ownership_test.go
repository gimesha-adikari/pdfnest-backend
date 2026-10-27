package storage

import (
	"bytes"
	"encoding/json"
	"github.com/gofiber/fiber/v2"
	"github.com/stretchr/testify/require"
	"net/http/httptest"
	"testing"
)

func TestUploadDestinationIsNewAndOwnerScoped(t *testing.T) {
	victim := NewOwnedKey("victim", "image_to_text_pdf", ".png")
	req := PresignRequest{Purpose: "image_to_text_pdf", Prefix: "studio/sources", Files: []PresignFile{{Name: "image.png", Size: 10, Key: victim}}}
	first, err := prepareOwnedUploads(req, "attacker")
	require.NoError(t, err)
	second, err := prepareOwnedUploads(req, "attacker")
	require.NoError(t, err)
	require.NotEqual(t, victim, first.Files[0].Key)
	require.NotEqual(t, first.Files[0].Key, second.Files[0].Key)
	require.True(t, IsOwnedKey(first.Files[0].Key, "attacker", "image_to_text_pdf"))
	require.False(t, IsOwnedKey(first.Files[0].Key, "victim", "image_to_text_pdf"))
	require.False(t, IsOwnedKey(first.Files[0].Key, "attacker", "editor_source"))
	require.Equal(t, victim, req.Files[0].Key)
	for _, key := range []string{victim + "/../../escape", "/" + victim, OwnedPrefix("victim", "image_to_text_pdf") + "-other/file"} {
		require.False(t, IsOwnedKey(key, "victim", "image_to_text_pdf"))
	}
}

func TestStudioPresignRejectsOver50MiB(t *testing.T) {
	_, err := prepareOwnedUploads(PresignRequest{Purpose: "studio_source", Files: []PresignFile{{Name: "source.pdf", Size: (50 << 20) + 1}}}, "synthetic-owner")
	require.Error(t, err, "Studio must not issue an oversized source declaration")
}

func TestStudioPresignBoundariesAndOtherPurposes(t *testing.T) {
	for _, purpose := range []string{"studio_source", "image_to_text_pdf", "repository_analyzer"} {
		limit := int64(250 << 20)
		if purpose == "studio_source" {
			limit = 50 << 20
		}
		for _, size := range []int64{-1, 0, limit - 1, limit, limit + 1} {
			req := PresignRequest{Purpose: purpose, Files: []PresignFile{{Name: "source.pdf", Size: size, Key: "studio/sources/chosen.pdf"}}}
			result, err := prepareOwnedUploads(req, "owner")
			if size <= 0 || size > limit {
				require.Error(t, err)
				continue
			}
			require.NoError(t, err)
			require.True(t, IsOwnedKey(result.Files[0].Key, "owner", purpose))
			require.NotEqual(t, req.Files[0].Key, result.Files[0].Key)
		}
	}
}

func TestStudioPresignOversizePublicContract(t *testing.T) {
	app := fiber.New()
	app.Use(func(c *fiber.Ctx) error { c.Locals("user_id", "synthetic-owner"); return c.Next() })
	app.Post("/presign", NewController().PresignUploads)
	body, err := json.Marshal(PresignRequest{Purpose: "studio_source", Files: []PresignFile{{Name: "source.pdf", Size: (50 << 20) + 1}}})
	require.NoError(t, err)
	req := httptest.NewRequest("POST", "/presign", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := app.Test(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, 400, resp.StatusCode)
	var result APIError
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&result))
	require.Equal(t, "INVALID_UPLOAD_SCOPE", result.Code)
}
