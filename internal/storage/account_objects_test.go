package storage

import (
	"context"
	"github.com/stretchr/testify/require"
	"os"
	"path/filepath"
	"testing"
)

func TestListOwnedObjectsPagesWithoutOtherOwnersOrSymlinks(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	root := t.TempDir()
	t.Setenv("LOCAL_STORAGE_DIR", root)
	owner := "owner-a"
	for _, key := range []string{OwnedPrefix(owner, "studio_source") + "/a.pdf", OwnedPrefix(owner, "studio_source") + "/b.pdf", OwnedPrefix("owner-b", "studio_source") + "/c.pdf"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, key)), 0700))
		require.NoError(t, os.WriteFile(filepath.Join(root, key), []byte("x"), 0600))
	}
	keys, cursor, err := ListOwnedObjects(context.Background(), owner, "studio_source", "", 1)
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.NotEmpty(t, cursor)
	more, end, err := ListOwnedObjects(context.Background(), owner, "studio_source", cursor, 1)
	require.NoError(t, err)
	require.Len(t, more, 1)
	require.Empty(t, end)
	require.NotEqual(t, keys[0], more[0])
	_, _, err = ListOwnedObjects(context.Background(), owner, "other", "", 1)
	require.Error(t, err)
	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "keep"), []byte("x"), 0600))
	link := OwnedPrefix(owner, "repository_analyzer")
	require.NoError(t, os.Symlink(outside, filepath.Join(root, link)))
	_, _, err = ListOwnedObjects(context.Background(), owner, "repository_analyzer", "", 1)
	require.Error(t, err)
}

func TestDeleteLocalObjectRejectsSymlinkParentEscape(t *testing.T) {
	t.Setenv("APP_ENV", "test")
	t.Setenv("STORAGE_MODE", "local")
	root := t.TempDir()
	t.Setenv("LOCAL_STORAGE_DIR", root)
	outside := t.TempDir()
	target := filepath.Join(outside, "keep.pdf")
	require.NoError(t, os.WriteFile(target, []byte("keep"), 0600))
	require.NoError(t, os.Symlink(outside, filepath.Join(root, "alias")))
	require.Error(t, DeleteLocalObject("alias/keep.pdf"))
	body, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "keep", string(body))
	require.NoError(t, os.WriteFile(filepath.Join(root, "delete.pdf"), []byte("gone"), 0600))
	require.NoError(t, DeleteLocalObject("delete.pdf"))
	require.NoError(t, DeleteLocalObject("delete.pdf"))
	require.NoError(t, os.Mkdir(filepath.Join(root, "empty"), 0700))
	require.NoError(t, DeleteLocalObject("empty"))
	require.NoError(t, os.Mkdir(filepath.Join(root, "nonempty"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "nonempty", "keep"), []byte("x"), 0600))
	require.Error(t, DeleteLocalObject("nonempty"))
}
