package auth

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestMissingAuthConfigurationReturnsError(t *testing.T) {
	t.Setenv("JWT_SECRET", "")
	t.Setenv("GOOGLE_CLIENT_ID", "")
	token, err := NewService().GenerateToken("user", "user")
	require.Error(t, err)
	require.Empty(t, token)
	claims, err := NewService().VerifyGoogleToken(context.Background(), "invalid")
	require.Error(t, err)
	require.Nil(t, claims)
}
