package storage

import (
	"errors"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/require"
)

func TestMissingObjectErrorsAreDesiredState(t *testing.T) {
	for _, code := range []string{"NoSuchKey", "NoSuchObject", "NotFound", "404"} {
		err := minio.ErrorResponse{Code: code, StatusCode: 404, Message: "missing"}
		require.True(t, isMissingObjectError(err), code)
	}
	require.False(t, isMissingObjectError(errors.New("temporary R2 timeout")))
}

func TestR2DeleteTimeoutIsBounded(t *testing.T) {
	require.Equal(t, 2*time.Minute, r2DeleteTimeout)
}
