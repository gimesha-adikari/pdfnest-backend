package uploads

import (
	"errors"
	"github.com/stretchr/testify/require"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type failedUploadReader struct{}

func (failedUploadReader) Read([]byte) (int, error) { return 0, errors.New("interrupted upload") }
func TestStagingFailureRemovesPartialFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "partial.pdf")
	err := copyStagedUpload(io.MultiReader(strings.NewReader("%PDF-partial"), failedUploadReader{}), path)
	require.Error(t, err)
	_, err = os.Stat(path)
	require.True(t, os.IsNotExist(err))
	require.NoError(t, copyStagedUpload(strings.NewReader("%PDF-success"), path))
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "%PDF-success", string(data))
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0600), info.Mode().Perm())
	require.Error(t, copyStagedUpload(strings.NewReader("overwrite"), path))
	data, err = os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "%PDF-success", string(data))
}
