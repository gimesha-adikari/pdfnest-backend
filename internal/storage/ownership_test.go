package storage

import (
	"github.com/stretchr/testify/require"
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
