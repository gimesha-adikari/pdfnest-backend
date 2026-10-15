package ocr

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

// Dormant trust boundary: a future server adapter must verify its authenticated
// request and return the canonical owner, never echo an owner supplied by a client.
// There is deliberately no production adapter or route using this interface.
type cancellationIdentityVerifier interface {
	VerifyCancellationIdentity(context.Context) (string, error)
}

type cancellationAuthority struct {
	owner      string
	verified   bool
	capability string
}

func ownerCancellation(ctx context.Context, verifier cancellationIdentityVerifier) (cancellationAuthority, error) {
	if verifier == nil {
		return cancellationAuthority{}, errFinalizationBinding
	}
	owner, err := verifier.VerifyCancellationIdentity(ctx)
	if err != nil {
		return cancellationAuthority{}, err
	}
	if owner == "" {
		return cancellationAuthority{}, errFinalizationBinding
	}
	return cancellationAuthority{owner: owner, verified: true}, nil
}

// The task-specific capability is generated at acceptance. Only its digest is
// stored; it is neither a worker claim nor permission to inspect billing/storage.
func capabilityCancellation(token string) cancellationAuthority {
	return cancellationAuthority{capability: token}
}

func (a cancellationAuthority) permits(j *ocrFinalization) bool {
	if a.verified && a.owner != "" && a.owner == j.OwnerID {
		return true
	}
	if len(a.capability) != 64 {
		return false
	}
	digest := capabilityDigest(a.capability)
	return subtle.ConstantTimeCompare([]byte(digest), []byte(j.CancellationCapabilityHash)) == 1
}

func randomAuthority() (string, error) {
	var value [32]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}

func capabilityDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

// A separate trusted adapter attests an internal deadline/recovery invocation.
// The repository additionally enforces the stored deadline under the row lock.
// No scheduler or reconciler is installed by this foundation.
type deadlineVerifier interface {
	VerifyDeadlineTask(context.Context) (string, error)
}
type deadlineAuthority struct {
	taskID   string
	verified bool
}

func trustedDeadline(ctx context.Context, verifier deadlineVerifier) (deadlineAuthority, error) {
	if verifier == nil {
		return deadlineAuthority{}, errFinalizationBinding
	}
	task, err := verifier.VerifyDeadlineTask(ctx)
	if err != nil {
		return deadlineAuthority{}, err
	}
	if task == "" {
		return deadlineAuthority{}, errFinalizationBinding
	}
	return deadlineAuthority{taskID: task, verified: true}, nil
}
