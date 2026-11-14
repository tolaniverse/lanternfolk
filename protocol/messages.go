// SPDX-License-Identifier: Apache-2.0

package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// Slice 1 (the workspace loop): sign in, create or join a workspace, chat,
// and see who is online. HTTP carries auth, create, and join; the WebSocket
// carries everything live.
//
//	POST /v1/auth/dev                 AuthDevRequest → AuthResponse (dev servers only)
//	POST /v1/workspaces               CreateWorkspace → Workspace
//	GET  /v1/workspaces/{slug}/gates  → []GatePrompt
//	POST /v1/workspaces/{slug}/join   JoinWorkspace → Workspace
//	GET  /v1/ws?workspace={id}&since={seq}   (Authorization: Bearer <token>)
//
// On the WebSocket the server first sends either KindSnapshot (since=0, or
// the gap is too old) or the missed KindEvent frames, then live frames.

// Command types (Envelope.Type with KindCommand).
const (
	TypeSendMessage = "SendMessage"
)

// Event types (Envelope.Type with KindEvent).
const (
	TypeMemberJoined   = "MemberJoined"
	TypeChannelCreated = "ChannelCreated"
	TypeMessagePosted  = "MessagePosted"
)

// Limits checked on both sides.
const (
	MaxMessageRunes   = 4000
	MaxGates          = 3
	MaxWorkspaceRunes = 64
)

var slugRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{1,30}[a-z0-9])$`)
var handleRe = regexp.MustCompile(`^[a-z0-9_]{2,24}$`)

// ---- HTTP bodies ----

// AuthDevRequest signs in by handle alone. Only servers started with dev
// auth accept it; real sign-in (OAuth device grant) replaces it later.
type AuthDevRequest struct {
	Handle string `json:"handle"`
}

// AuthResponse carries the bearer token for later requests.
type AuthResponse struct {
	Token  string `json:"token"`
	UserID string `json:"user_id"`
	Handle string `json:"handle"`
}

// GateKind is how a gate checks an answer. All answers are compared after
// trimming and lowercasing, and stored only as hashes.
type GateKind string

const (
	GateInviteCode   GateKind = "invite_code"
	GateSecretPhrase GateKind = "secret_phrase" // "open sesame"
	GateRiddle       GateKind = "riddle"        // the dungeon keeper's question
)

// GateSpec defines a gate when creating a workspace. Answer is sent once and
// never returned.
type GateSpec struct {
	Kind   GateKind `json:"kind"`
	Prompt string   `json:"prompt"`
	Answer string   `json:"answer"`
}

// GatePrompt is what a joiner sees for each gate, in order.
type GatePrompt struct {
	Kind   GateKind `json:"kind"`
	Prompt string   `json:"prompt"`
}

// CreateWorkspace makes a workspace with a #general channel; the creator
// becomes its owner.
type CreateWorkspace struct {
	Name  string     `json:"name"`
	Slug  string     `json:"slug"`
	Gates []GateSpec `json:"gates,omitempty"`
}

// JoinWorkspace answers the workspace's gates, in order.
type JoinWorkspace struct {
	Answers []string `json:"answers"`
}

// ---- domain views ----

type Workspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type Role string

const (
	RoleOwner  Role = "owner"
	RoleMember Role = "member"
)

type Member struct {
	UserID string `json:"user_id"`
	Handle string `json:"handle"`
	Role   Role   `json:"role"`
}

type Channel struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type Message struct {
	ID          string    `json:"id"` // UUIDv7: sorts by creation time
	ChannelID   string    `json:"channel_id"`
	AuthorID    string    `json:"author_id"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"created_at"`
	ClientCmdID string    `json:"client_cmd_id,omitempty"` // lets the author reconcile its optimistic copy
}

// ---- WebSocket payloads ----

// SendMessage posts to a channel.
type SendMessage struct {
	ChannelID string `json:"channel_id"`
	Body      string `json:"body"`
}

type MemberJoined struct {
	Member Member `json:"member"`
}

type ChannelCreated struct {
	Channel Channel `json:"channel"`
}

type MessagePosted struct {
	Message Message `json:"message"`
}

// Snapshot is the full client-visible state at Seq. Messages holds the most
// recent ones per channel (oldest first); older history is paged over HTTP
// later.
type Snapshot struct {
	Seq       uint64    `json:"seq"`
	Workspace Workspace `json:"workspace"`
	Members   []Member  `json:"members"`
	Channels  []Channel `json:"channels"`
	Messages  []Message `json:"messages"`
	Online    []string  `json:"online"` // user IDs
}

// PresenceStatus is a member's liveness.
type PresenceStatus string

const (
	Online  PresenceStatus = "online"
	Offline PresenceStatus = "offline"
)

// Presence is the payload of a KindPresence frame: what changed since the
// last tick. Presence is never stored and has no Seq.
type Presence struct {
	Changes []PresenceChange `json:"changes"`
}

type PresenceChange struct {
	UserID string         `json:"user_id"`
	Status PresenceStatus `json:"status"`
}

// ---- validation ----

var ErrInvalidPayload = errors.New("protocol: invalid payload")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidPayload, fmt.Sprintf(format, a...))
}

// ValidHandle reports whether h is a usable handle (2–24 of a-z, 0-9, _).
func ValidHandle(h string) bool { return handleRe.MatchString(h) }

// Validate checks a CreateWorkspace request.
func (c CreateWorkspace) Validate() error {
	n := utf8.RuneCountInString(strings.TrimSpace(c.Name))
	if n == 0 || n > MaxWorkspaceRunes {
		return invalid("name must be 1–%d characters", MaxWorkspaceRunes)
	}
	if !slugRe.MatchString(c.Slug) {
		return invalid("slug must be 3–32 of a-z, 0-9, and inner dashes")
	}
	if len(c.Gates) > MaxGates {
		return invalid("at most %d gates", MaxGates)
	}
	for i, g := range c.Gates {
		switch g.Kind {
		case GateInviteCode, GateSecretPhrase, GateRiddle:
		default:
			return invalid("gate %d: unknown kind %q", i, g.Kind)
		}
		if strings.TrimSpace(g.Prompt) == "" || NormalizeAnswer(g.Answer) == "" {
			return invalid("gate %d: needs a prompt and an answer", i)
		}
	}
	return nil
}

// Validate checks a SendMessage command.
func (s SendMessage) Validate() error {
	if s.ChannelID == "" {
		return invalid("channel_id is required")
	}
	n := utf8.RuneCountInString(strings.TrimSpace(s.Body))
	if n == 0 || n > MaxMessageRunes {
		return invalid("body must be 1–%d characters", MaxMessageRunes)
	}
	return nil
}

// NormalizeAnswer is how gate answers are compared: trimmed, lowercased,
// inner whitespace collapsed. "  Open   Sesame " == "open sesame".
func NormalizeAnswer(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(s)), " ")
}

// ---- envelope helpers ----

// NewCommand wraps a payload as a command frame.
func NewCommand(typ, clientCmdID, workspaceID string, payload any) (Envelope, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return Envelope{}, err
	}
	return Envelope{V: Version, Kind: KindCommand, Type: typ, ClientCmdID: clientCmdID,
		WorkspaceID: workspaceID, Payload: b}, nil
}

// DecodePayload unmarshals an envelope's payload into T.
func DecodePayload[T any](e Envelope) (T, error) {
	var v T
	if err := json.Unmarshal(e.Payload, &v); err != nil {
		return v, invalid("%s payload: %v", e.Type, err)
	}
	return v, nil
}
