// Package auth resolves SSH connections into AgentBBS identities (PRD §4.4).
package auth

import (
	"strings"

	"github.com/charmbracelet/ssh"
	gossh "golang.org/x/crypto/ssh"
)

// Kind classifies an identity.
type Kind string

const (
	Guest  Kind = "guest"
	Member Kind = "member"
	Agent  Kind = "agent"
)

// User is the resolved identity for one session.
type User struct {
	Name      string
	Kind      Kind
	PubKeyFP  string // SHA256 fingerprint, empty for guests without a key
	StoreID   int64  // 0 for guests
}

// GuestNames are usernames that always map to an anonymous guest hub session.
var GuestNames = map[string]bool{"bbs": true, "play": true, "guest": true}

// PodNames are usernames that route to a personal pod instead of the hub.
// Pod access requires an active paid membership (PRD pods addendum).
var PodNames = map[string]bool{"pod": true}

// JoinNames are usernames that trigger the onboarding flow: register the
// visitor's public key, print instructions, and disconnect.
var JoinNames = map[string]bool{"join": true, "signup": true, "register": true}

// DomainNames are usernames that route to the custom-domain self-service flow:
// list/add/remove the domains pointed at a member's homepage.
var DomainNames = map[string]bool{"domain": true, "domains": true}

// IsGuestName reports whether the SSH username requests anonymous hub access.
func IsGuestName(u string) bool { return GuestNames[strings.ToLower(u)] }

// IsPodName reports whether the SSH username requests the pod route.
func IsPodName(u string) bool { return PodNames[strings.ToLower(u)] }

// IsJoinName reports whether the SSH username requests onboarding.
func IsJoinName(u string) bool { return JoinNames[strings.ToLower(u)] }

// IsDomainName reports whether the SSH username requests the custom-domain flow.
func IsDomainName(u string) bool { return DomainNames[strings.ToLower(u)] }

// KindFor infers the identity kind from a (non-guest) username.
// Usernames prefixed "agent-" are automated clients (PRD §3).
func KindFor(username string) Kind {
	if strings.HasPrefix(strings.ToLower(username), "agent-") {
		return Agent
	}
	return Member
}

// Fingerprint returns the SHA256 fingerprint for a session public key, or "".
func Fingerprint(key ssh.PublicKey) string {
	if key == nil {
		return ""
	}
	return gossh.FingerprintSHA256(key)
}
