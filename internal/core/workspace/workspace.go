// Package workspace holds the workspace rules: roles and entry gates.
// It is pure domain code: standard library only. Hashing is injected
// through the Hasher port so the core does not depend on a crypto library.
package workspace

import (
	"crypto/subtle"
	"errors"
	"fmt"
)

// Role is a member's role in a workspace.
type Role string

const (
	Owner  Role = "owner"
	Member Role = "member"
)

// CanPost reports whether a role may post messages. Every role can today;
// guests and read-only roles will be added here.
func (r Role) CanPost() bool { return r == Owner || r == Member }

// Gate is one entry check: an invite code, a secret phrase, or a riddle.
// Only a salted hash of the normalized answer is kept.
type Gate struct {
	Kind   string
	Prompt string
	Salt   []byte
	Hash   []byte
}

// Hasher derives the stored hash of a normalized answer.
type Hasher interface {
	Hash(normalizedAnswer string, salt []byte) []byte
}

// ErrGateFailed is returned for a missing or wrong answer.
var ErrGateFailed = errors.New("workspace: the gate keeper is not convinced")

// CheckGates verifies one normalized answer per gate, in order. Every gate
// is hashed even after a failure, so timing does not reveal which answer
// was wrong.
func CheckGates(gates []Gate, normalizedAnswers []string, h Hasher) error {
	if len(normalizedAnswers) != len(gates) {
		return fmt.Errorf("%w: want %d answers, got %d", ErrGateFailed, len(gates), len(normalizedAnswers))
	}
	ok := 1
	for i, g := range gates {
		ok &= subtle.ConstantTimeCompare(h.Hash(normalizedAnswers[i], g.Salt), g.Hash)
	}
	if ok != 1 {
		return ErrGateFailed
	}
	return nil
}
