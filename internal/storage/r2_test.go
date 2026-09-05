package storage

import (
	"context"
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

func TestRetentionLockedErrorIsStructuredAndDoesNotMatchGenericFailures(t *testing.T) {
	retentionProviderError := minio.ErrorResponse{Code: r2RetentionLockedErrorCode, StatusCode: 409, Message: "retention policy"}
	require.True(t, isRetentionLockedObjectError(retentionProviderError))

	retentionErr := &RetentionLockedError{ProviderCode: r2RetentionLockedErrorCode}
	require.True(t, IsRetentionLockedError(retentionErr))
	require.Equal(t, "object deletion deferred by provider retention", retentionErr.Error())

	permissionErr := minio.ErrorResponse{Code: "AccessDenied", StatusCode: 403, Message: "access denied"}
	require.False(t, isRetentionLockedObjectError(permissionErr))
	require.False(t, IsRetentionLockedError(permissionErr))
	require.False(t, IsRetentionLockedError(errors.New("transient R2 timeout")))
}

func TestR2DeleteRejectsUnsafeKeyBeforeStorageRequest(t *testing.T) {
	err := (&Store{}).DeleteObject(context.Background(), "../..")
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid storage key")
	require.False(t, IsRetentionLockedError(err))
}

func TestR2DeleteTimeoutIsBounded(t *testing.T) {
	require.Equal(t, 2*time.Minute, r2DeleteTimeout)
}
