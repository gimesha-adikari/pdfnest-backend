//go:build !linux

package storage

import "context"

type localCreateOps struct{}

func createLocalArtifact(context.Context, string, string, string, string, int64, localCreateOps) error {
	return createError(ArtifactCreateUnsupported)
}
