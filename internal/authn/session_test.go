package authn

import (
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"
)

func TestSessionVersionClaimDefaultsAndRejectsMalformedValues(t *testing.T) {
	version, err := sessionVersionClaim(jwt.MapClaims{})
	require.NoError(t, err)
	require.Zero(t, version)

	version, err = sessionVersionClaim(jwt.MapClaims{"session_version": float64(4)})
	require.NoError(t, err)
	require.Equal(t, int64(4), version)

	_, err = sessionVersionClaim(jwt.MapClaims{"session_version": float64(1.5)})
	require.Error(t, err)
	_, err = sessionVersionClaim(jwt.MapClaims{"session_version": "4"})
	require.Error(t, err)
}
