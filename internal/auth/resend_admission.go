package auth

import (
	"github.com/gofiber/fiber/v2"
	"sync"
	"time"
)

const resendPeerCapacity = 4096
const resendPeerRequests = 10

type resendWindow struct {
	until time.Time
	count int
}
type verificationAdmission struct {
	mu    sync.Mutex
	peers map[string]resendWindow
}

func newVerificationAdmission() *verificationAdmission {
	return &verificationAdmission{peers: make(map[string]resendWindow)}
}

// A bounded per-process peer-IP brake, independent of account existence. Only
// the transport peer is used, never client-supplied forwarding headers. The
// account cooldown itself is durable and shared across replicas in PostgreSQL.
func (g *verificationAdmission) allow(peer string, now time.Time) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	window, exists := g.peers[peer]
	if exists && !now.Before(window.until) {
		delete(g.peers, peer)
		exists = false
	}
	if !exists {
		if len(g.peers) >= resendPeerCapacity {
			for key, value := range g.peers {
				if !now.Before(value.until) {
					delete(g.peers, key)
				}
			}
			if len(g.peers) >= resendPeerCapacity {
				return false
			}
		}
		window = resendWindow{until: now.Add(time.Minute)}
	}
	if window.count >= resendPeerRequests {
		return false
	}
	window.count++
	g.peers[peer] = window
	return true
}
func verificationResendResponse(c *fiber.Ctx) error {
	return c.JSON(fiber.Map{"success": true, "message": "Verification email sent"})
}
