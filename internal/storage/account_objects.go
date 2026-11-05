package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
)

// ListOwnedObjects enumerates only a server-issued authenticated-owner namespace.
// The opaque cursor is the last canonical key; no bucket-wide or recursive local
// scan is permitted. Upload destinations in these namespaces are flat UUID keys.
func ListOwnedObjects(ctx context.Context, owner, purpose, cursor string, limit int) ([]string, string, error) {
	if owner == "" {
		return nil, "", fmt.Errorf("owner required")
	}
	switch purpose {
	case "studio_source", "repository_analyzer", "image_to_text_pdf":
	default:
		return nil, "", fmt.Errorf("unsupported namespace")
	}
	if limit < 1 || limit > 100 {
		return nil, "", fmt.Errorf("invalid page limit")
	}
	prefix := OwnedPrefix(owner, purpose) + "/"
	if cursor != "" && (!IsOwnedKey(cursor, owner, purpose) || strings.Contains(strings.TrimPrefix(cursor, prefix), "/")) {
		return nil, "", fmt.Errorf("invalid cursor")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var keys []string
	if RemoteStorageEnabled() {
		store, err := Default()
		if err != nil {
			return nil, "", err
		}
		if store == nil {
			return nil, "", fmt.Errorf("storage unavailable")
		}
		for object := range store.client.ListObjects(ctx, store.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true, StartAfter: cursor, MaxKeys: limit + 1}) {
			if object.Err != nil {
				return nil, "", object.Err
			}
			if !IsOwnedKey(object.Key, owner, purpose) {
				return nil, "", fmt.Errorf("unexpected namespace")
			}
			keys = append(keys, object.Key)
			if len(keys) > limit {
				break
			}
		}
	} else {
		root, err := os.OpenRoot(GetLocalStorageDir())
		if err != nil {
			return nil, "", err
		}
		defer root.Close()
		parts := strings.Split(strings.TrimSuffix(prefix, "/"), "/")
		for i := range parts {
			info, e := root.Lstat(strings.Join(parts[:i+1], "/"))
			if os.IsNotExist(e) {
				return nil, "", nil
			}
			if e != nil {
				return nil, "", e
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return nil, "", fmt.Errorf("unsafe namespace")
			}
		}
		dir, err := root.Open(strings.TrimSuffix(prefix, "/"))
		if err != nil {
			return nil, "", err
		}
		defer dir.Close()
		// Read in bounded batches and retain only the smallest page after cursor.
		for {
			entries, e := dir.ReadDir(100)
			for _, entry := range entries {
				if err := ctx.Err(); err != nil {
					return nil, "", err
				}
				if !entry.Type().IsRegular() {
					return nil, "", fmt.Errorf("unexpected namespace entry")
				}
				key := prefix + entry.Name()
				if key <= cursor {
					continue
				}
				keys = append(keys, key)
				sort.Strings(keys)
				if len(keys) > limit+1 {
					keys = keys[:limit+1]
				}
			}
			if e == io.EOF {
				break
			}
			if e != nil {
				return nil, "", e
			}
		}
	}
	if len(keys) > limit {
		return keys[:limit], keys[limit-1], nil
	}
	return keys, "", ctx.Err()
}
