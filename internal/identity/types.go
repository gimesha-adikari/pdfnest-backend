package identity

import "time"

type Type string

const (
	TypeGuest Type = "guest"
	TypeUser  Type = "user"
)

const (
	CookieGuestID      = "platen_guest"
	HeaderFingerprint  = "X-Platen-Fingerprint"
	LocalIdentityKey   = "identity"
	LocalIdentityIDKey = "identity_id"
	LocalIdentityType  = "identity_type"
	LocalUserIDKey     = "user_id"
	LocalUserRoleKey   = "role"
)

type Identity struct {
	ID              string    `json:"id"`
	Type            Type      `json:"type"`
	Role            string    `json:"role,omitempty"`
	GuestCookie     string    `json:"guest_cookie,omitempty"`
	FingerprintHash string    `json:"fingerprint_hash,omitempty"`
	UserAgentHash   string    `json:"user_agent_hash,omitempty"`
	IPHash          string    `json:"ip_hash,omitempty"`
	Trust           int       `json:"trust"`
	CreatedAt       time.Time `json:"created_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
}

func (i Identity) IsGuest() bool { return i.Type == TypeGuest }
func (i Identity) IsUser() bool  { return i.Type == TypeUser }

type GuestRecord struct {
	ID              string    `json:"id"`
	FingerprintHash string    `json:"fingerprint_hash,omitempty"`
	UserAgentHash   string    `json:"user_agent_hash,omitempty"`
	IPHash          string    `json:"ip_hash,omitempty"`
	Trust           int       `json:"trust"`
	CreatedAt       time.Time `json:"created_at"`
	LastSeenAt      time.Time `json:"last_seen_at"`
}
