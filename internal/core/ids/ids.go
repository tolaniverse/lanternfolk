// Package ids defines Lanternfolk's identifier types.
//
// IDs are UUIDv7 (RFC 9562): a 48-bit Unix millisecond timestamp followed by
// random bits. They sort by creation time, so ORDER BY id is creation order
// and a pagination cursor is just an ID. Each entity has its own type, so a
// UserID cannot be passed where a WorkspaceID is expected.
//
// This package is part of the domain core and imports only the standard
// library (enforced by internal/archtest).
package ids

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"time"
)

// UUID is a 128-bit identifier in RFC 9562 layout.
type UUID [16]byte

// ErrInvalid reports a string that is not a canonical UUID.
var ErrInvalid = errors.New("ids: invalid UUID")

// NewV7 returns a fresh version 7 UUID for the current time.
func NewV7() UUID { return newV7(time.Now()) }

func newV7(now time.Time) UUID {
	var u UUID
	ms := uint64(now.UnixMilli())
	var ts [8]byte
	binary.BigEndian.PutUint64(ts[:], ms)
	copy(u[0:6], ts[2:8]) // 48-bit big-endian millisecond timestamp
	if _, err := rand.Read(u[6:]); err != nil {
		panic("ids: crypto/rand failed: " + err.Error()) // unreachable on supported platforms
	}
	u[6] = (u[6] & 0x0f) | 0x70 // version 7
	u[8] = (u[8] & 0x3f) | 0x80 // RFC 9562 variant
	return u
}

// Time returns the millisecond timestamp embedded in a version 7 UUID.
func (u UUID) Time() time.Time {
	var ts [8]byte
	copy(ts[2:8], u[0:6])
	return time.UnixMilli(int64(binary.BigEndian.Uint64(ts[:])))
}

// IsZero reports whether u is the all-zero UUID.
func (u UUID) IsZero() bool { return u == UUID{} }

// String returns the canonical 8-4-4-4-12 hex form.
func (u UUID) String() string {
	var b [36]byte
	hex.Encode(b[0:8], u[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], u[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], u[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], u[8:10])
	b[23] = '-'
	hex.Encode(b[24:36], u[10:16])
	return string(b[:])
}

// Parse reads the canonical lowercase or uppercase 8-4-4-4-12 form.
func Parse(s string) (UUID, error) {
	var u UUID
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' {
		return u, ErrInvalid
	}
	groups := [5][2]int{{0, 8}, {9, 13}, {14, 18}, {19, 23}, {24, 36}}
	at := 0
	for _, g := range groups {
		n, err := hex.Decode(u[at:], []byte(s[g[0]:g[1]]))
		if err != nil {
			return UUID{}, ErrInvalid
		}
		at += n
	}
	return u, nil
}

// MarshalText implements encoding.TextMarshaler (used by encoding/json).
func (u UUID) MarshalText() ([]byte, error) { return []byte(u.String()), nil }

// UnmarshalText implements encoding.TextUnmarshaler.
func (u *UUID) UnmarshalText(b []byte) error {
	p, err := Parse(string(b))
	if err != nil {
		return err
	}
	*u = p
	return nil
}

// Typed IDs. Each is a distinct type over UUID; convert explicitly.
type (
	UserID      UUID
	AgentID     UUID
	OrgID       UUID
	WorkspaceID UUID
	ChannelID   UUID
	MessageID   UUID
	RunID       UUID
)

func (id UserID) String() string      { return UUID(id).String() }
func (id AgentID) String() string     { return UUID(id).String() }
func (id OrgID) String() string       { return UUID(id).String() }
func (id WorkspaceID) String() string { return UUID(id).String() }
func (id ChannelID) String() string   { return UUID(id).String() }
func (id MessageID) String() string   { return UUID(id).String() }
func (id RunID) String() string       { return UUID(id).String() }

// Seq is a workspace's event sequence number: one total order per workspace,
// and the cursor a client resumes from after reconnecting.
type Seq uint64
