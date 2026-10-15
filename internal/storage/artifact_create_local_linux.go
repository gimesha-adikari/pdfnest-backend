//go:build linux

package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// Test-only injection is private and never selected through environment/config.
type localCreateOps struct {
	copy          func(io.Writer, io.Reader) (int64, error)
	fileSync      func(*os.File) error
	directorySync func(int) error
	publish       func(int, string, string) error
	staged        func()
}

func createLocalArtifact(ctx context.Context, root, key, source, digest string, size int64, ops localCreateOps) (result error) {
	if !validCreateSource(key, digest, size) {
		return createError(ArtifactCreateInvalid)
	}
	if ctx.Err() != nil {
		return errors.Join(createError(ArtifactCreateNotApplied), ctx.Err())
	}
	input, err := os.Open(source)
	if err != nil {
		return errors.Join(createError(ArtifactCreateNotApplied), ctx.Err())
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() != size {
		return createError(ArtifactCreateInvalid)
	}
	// Each component is opened relative to an already held directory descriptor.
	// O_NOFOLLOW rejects even an in-root symlink alias to another task namespace.
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return localCreateFailure(err)
	}
	dirs := []int{fd}
	defer func() {
		for _, dir := range dirs {
			_ = unix.Close(dir)
		}
	}()
	parts := strings.Split(key, "/")
	for _, part := range parts[:len(parts)-1] {
		err = unix.Mkdirat(fd, part, 0700)
		if err != nil && !errors.Is(err, unix.EEXIST) {
			return localCreateFailure(err)
		}
		next, openErr := unix.Openat(fd, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			return localCreateFailure(openErr)
		}
		dirs = append(dirs, next)
		fd = next
	}
	leaf := parts[len(parts)-1]
	// One staging slot per intent bounds crash debris and fences overlapping
	// attempts across processes. A surviving slot is uncertain, not absent.
	stage := ".ocr-stage-" + leaf
	outFD, err := unix.Openat(fd, stage, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if errors.Is(err, unix.EEXIST) {
		return createError(ArtifactCreateUncertain)
	}
	if err != nil {
		return localCreateFailure(err)
	}
	output := os.NewFile(uintptr(outFD), stage)
	stageOwned := true
	defer func() {
		_ = output.Close()
		if stageOwned {
			if err := unix.Unlinkat(fd, stage, 0); err != nil && !errors.Is(err, unix.ENOENT) {
				result = errors.Join(result, createError(ArtifactCreateUncertain))
			}
		}
	}()
	hash := sha256.New()
	copyBytes := ops.copy
	if copyBytes == nil {
		copyBytes = io.Copy
	}
	n, err := copyBytes(io.MultiWriter(output, hash), io.LimitReader(contextArtifactReader{ctx, input}, size+1))
	if err != nil {
		return errors.Join(createError(ArtifactCreateNotApplied), ctx.Err())
	}
	if n != size || hex.EncodeToString(hash.Sum(nil)) != digest {
		return createError(ArtifactCreateInvalid)
	}
	syncFile := ops.fileSync
	if syncFile == nil {
		syncFile = func(f *os.File) error { return f.Sync() }
	}
	if err = syncFile(output); err != nil {
		return errors.Join(createError(ArtifactCreateNotApplied), ctx.Err())
	}
	if err = output.Close(); err != nil {
		return errors.Join(createError(ArtifactCreateNotApplied), ctx.Err())
	}
	if ops.staged != nil {
		ops.staged()
	}
	if ctx.Err() != nil {
		return errors.Join(createError(ArtifactCreateNotApplied), ctx.Err())
	}
	publish := ops.publish
	if publish == nil {
		publish = func(dir int, from, to string) error { return unix.Linkat(dir, from, dir, to, 0) }
	}
	err = publish(fd, stage, leaf)
	if errors.Is(err, unix.EEXIST) {
		return createError(ArtifactCreateExists)
	}
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EXDEV) || errors.Is(err, unix.ENOSYS) {
		return createError(ArtifactCreateUnsupported)
	}
	if err != nil {
		return createError(ArtifactCreateUncertain)
	}
	// Final path now names only complete, synced bytes. Never remove it on error.
	if err = unix.Unlinkat(fd, stage, 0); err != nil {
		return createError(ArtifactCreateUncertain)
	}
	stageOwned = false
	syncDir := ops.directorySync
	if syncDir == nil {
		syncDir = unix.Fsync
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err = syncDir(dirs[i]); err != nil {
			return createError(ArtifactCreateUncertain)
		}
	}
	if ctx.Err() != nil {
		return errors.Join(createError(ArtifactCreateUncertain), ctx.Err())
	}
	return nil
}
func localCreateFailure(err error) error {
	if errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM) {
		return createError(ArtifactCreateDenied)
	}
	return createError(ArtifactCreateNotApplied)
}
