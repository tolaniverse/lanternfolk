// Package protocol is Lanternfolk's public, versioned wire contract between
// clients and lanternfolkd. It is the only package outside internal/, and it
// imports only the standard library (enforced by internal/archtest).
//
// Every WebSocket frame is an Envelope. Payload is decoded by Type.
package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Version is the protocol version this build speaks. The server accepts
// Version and Version-1 (System Design §10).
const Version = 1

// Kind says which lane a frame belongs to (System Design §4).
type Kind string

const (
	KindCommand  Kind = "cmd"             // client → server
	KindAck      Kind = "ack"             // server → client: confirms or rejects a command
	KindEvent    Kind = "event"           // durable lane: ordered by Seq, resumable
	KindPresence Kind = "presence"        // presence lane: coalesced, never stored
	KindStream   Kind = "stream"          // agent token deltas, lossy on reconnect
	KindResync   Kind = "resync_required" // client must fetch a fresh snapshot
)

// Envelope is one frame on the wire.
type Envelope struct {
	V           int             `json:"v"`
	Kind        Kind            `json:"kind"`
	WorkspaceID string          `json:"workspace_id,omitempty"`
	Seq         uint64          `json:"seq,omitempty"`           // set on KindEvent and on a successful KindAck
	ClientCmdID string          `json:"client_cmd_id,omitempty"` // set on KindCommand and KindAck
	Type        string          `json:"type,omitempty"`          // e.g. "SendMessage", "MessagePosted"
	Payload     json.RawMessage `json:"payload,omitempty"`
	Error       *Error          `json:"error,omitempty"` // set on a rejecting KindAck
}

// Error is a machine-readable rejection.
type Error struct {
	Code    string `json:"code"` // e.g. "forbidden", "invalid", "unavailable"
	Message string `json:"message,omitempty"`
}

// Errors returned by Decode.
var (
	ErrUnsupportedVersion = errors.New("protocol: unsupported version")
	ErrInvalidEnvelope    = errors.New("protocol: invalid envelope")
)

// Supported reports whether a peer's protocol version is accepted.
func Supported(v int) bool { return v == Version || v == Version-1 && v > 0 }

// Encode marshals an envelope, stamping the current Version if unset.
func Encode(e Envelope) ([]byte, error) {
	if e.V == 0 {
		e.V = Version
	}
	if err := e.validate(); err != nil {
		return nil, err
	}
	return json.Marshal(e)
}

// Decode unmarshals and validates one frame.
func Decode(b []byte) (Envelope, error) {
	var e Envelope
	if err := json.Unmarshal(b, &e); err != nil {
		return Envelope{}, fmt.Errorf("%w: %v", ErrInvalidEnvelope, err)
	}
	if !Supported(e.V) {
		return Envelope{}, fmt.Errorf("%w: %d", ErrUnsupportedVersion, e.V)
	}
	if err := e.validate(); err != nil {
		return Envelope{}, err
	}
	return e, nil
}

func (e Envelope) validate() error {
	switch e.Kind {
	case KindCommand:
		if e.ClientCmdID == "" || e.Type == "" {
			return fmt.Errorf("%w: cmd needs client_cmd_id and type", ErrInvalidEnvelope)
		}
	case KindAck:
		if e.ClientCmdID == "" {
			return fmt.Errorf("%w: ack needs client_cmd_id", ErrInvalidEnvelope)
		}
	case KindEvent:
		if e.Seq == 0 || e.Type == "" {
			return fmt.Errorf("%w: event needs seq and type", ErrInvalidEnvelope)
		}
	case KindPresence, KindStream, KindResync:
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalidEnvelope, e.Kind)
	}
	return nil
}
