package studio

import "errors"

var (
	ErrDocumentNotFound    = errors.New("studio document not found")
	ErrSessionNotFound     = errors.New("studio session not found")
	ErrSessionExpired      = errors.New("studio session expired")
	ErrUnauthorized        = errors.New("unauthorized studio session access")
	ErrVersionNotFound     = errors.New("studio version not found")
	ErrInvalidBaseVersion  = errors.New("base version does not match active session version")
	ErrConflict            = errors.New("optimistic concurrency conflict on session")
	ErrIdempotencyConflict = errors.New("idempotency key conflict with mismatched operation parameters")
	ErrAssetNotFound       = errors.New("studio asset not found")
	ErrSnapshotNotFound    = errors.New("studio snapshot not found")
	ErrInvalidOperation    = errors.New("invalid studio operation parameters")
	ErrNoParentVersion     = errors.New("cannot undo: version has no parent")
	ErrNoRedoChild         = errors.New("cannot redo: version has no child branch")
	ErrInvalidBranchTarget = errors.New("target version does not belong to this document")
)
