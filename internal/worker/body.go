package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"sync"
	"time"
)

// The platform admits 100 MiB uploads. Allow multipart/derived-document
// headroom without turning signing into an unbounded scratch-disk consumer.
const MaxRequestBodyBytes int64 = 128 << 20
const bodyMemoryBytes = 64 << 10
const bodyStageTimeout = 5 * time.Minute

var ErrRequestBodyTooLarge = errors.New("worker request exceeds 128 MiB body envelope")
var ErrBodySpoolCapacity = errors.New("worker request body staging capacity unavailable")
var bodySpoolSlots = make(chan struct{}, 4) // At most 512 MiB of signing spool data.

type stagedBody struct {
	*os.File
	once sync.Once
}

func (b *stagedBody) Close() error {
	var err error
	b.once.Do(func() {
		err = b.File.Close()
		_ = os.Remove(b.Name())
		<-bodySpoolSlots
	})
	return err
}

// Snapshot the actual serialized stream once. Closing the source also unblocks
// io.Pipe multipart producers on cancellation, limits and every other failure.
func stageRequestBody(ctx context.Context, source io.ReadCloser, limit int64) (result io.ReadCloser, digest string, size int64, err error) {
	ctx, cancel := context.WithTimeout(ctx, bodyStageTimeout)
	defer cancel()
	if source != nil {
		stop := context.AfterFunc(ctx, func() { _ = source.Close() })
		defer stop()
		defer source.Close()
	}
	hash := sha256.New()
	var memory bytes.Buffer
	var spool *stagedBody
	defer func() {
		if err != nil && spool != nil {
			_ = spool.Close()
		}
	}()
	buffer := make([]byte, 32<<10)
	for source != nil {
		if err = ctx.Err(); err != nil {
			return nil, "", 0, err
		}
		var n int
		var readErr error
		n, readErr = source.Read(buffer)
		if n > 0 {
			size += int64(n)
			if size > limit {
				return nil, "", 0, ErrRequestBodyTooLarge
			}
			if spool == nil && size > bodyMemoryBytes {
				select {
				case bodySpoolSlots <- struct{}{}:
				default:
					return nil, "", 0, ErrBodySpoolCapacity
				}
				var file *os.File
				file, err = os.CreateTemp("", "platen-worker-body-*") // 0600
				if err != nil {
					<-bodySpoolSlots
					return nil, "", 0, err
				}
				spool = &stagedBody{File: file}
				if _, err = spool.Write(memory.Bytes()); err != nil {
					return nil, "", 0, err
				}
				memory.Reset()
			}
			if spool != nil {
				_, err = spool.Write(buffer[:n])
			} else {
				_, err = memory.Write(buffer[:n])
			}
			if err != nil {
				return nil, "", 0, err
			}
			_, _ = hash.Write(buffer[:n])
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				if ctx.Err() != nil {
					readErr = ctx.Err()
				}
				return nil, "", 0, readErr
			}
			break
		}
	}
	if err = ctx.Err(); err != nil {
		return nil, "", 0, err
	}
	if spool != nil {
		if _, err = spool.Seek(0, io.SeekStart); err != nil {
			return nil, "", 0, err
		}
		result = spool
	} else {
		result = io.NopCloser(bytes.NewReader(memory.Bytes()))
	}
	return result, hex.EncodeToString(hash.Sum(nil)), size, nil
}
