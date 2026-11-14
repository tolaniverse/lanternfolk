// Package transport moves the wire protocol (package protocol) between the
// client and lanternfolkd: HTTP for auth, create, gates, and join, and a
// frame stream over the WebSocket.
//
// HTTP talks to a real server; FakeServer implements the same contract in
// memory for tests and the TUI's -demo mode. Link keeps a stream connected,
// reconnecting with jittered exponential backoff and since=<last seq>.
package transport

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/tolaniverse/lanternfolk/protocol"
)

// API is everything the client asks of a server. Both HTTP and FakeServer
// implement it.
type API interface {
	AuthDev(ctx context.Context, handle string) (protocol.AuthResponse, error)
	CreateWorkspace(ctx context.Context, token string, req protocol.CreateWorkspace) (protocol.Workspace, error)
	Gates(ctx context.Context, token, slug string) ([]protocol.GatePrompt, error)
	Join(ctx context.Context, token, slug string, req protocol.JoinWorkspace) (protocol.Workspace, error)
	Dialer
}

// Dialer opens a frame stream for one workspace. since=0 asks for a snapshot.
type Dialer interface {
	Dial(ctx context.Context, token, workspaceID string, since uint64) (Stream, error)
}

// Stream is one open WebSocket: one Envelope per frame. Recv must be called
// from one goroutine; Send and Close are safe to call from any.
type Stream interface {
	Recv(ctx context.Context) (protocol.Envelope, error)
	Send(ctx context.Context, e protocol.Envelope) error
	Close() error
}

// Error is a rejection from the server, over HTTP or at the WebSocket
// upgrade.
type Error struct {
	Status  int    // HTTP status
	Code    string // protocol.Error.Code, e.g. "forbidden"
	Message string
}

func (e *Error) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("server returned %d %s", e.Status, http.StatusText(e.Status))
}

// Permanent reports whether retrying cannot help (bad token, not a member,
// no such workspace), so a Link should stop instead of backing off.
func (e *Error) Permanent() bool {
	switch e.Status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return true
	}
	return false
}

// Error codes used by FakeServer, matching the HTTP status it pairs them with.
const (
	CodeInvalid      = "invalid"
	CodeUnauthorized = "unauthorized"
	CodeForbidden    = "forbidden"
	CodeNotFound     = "not_found"
	CodeConflict     = "conflict"
)

func errorFor(status int, code, format string, a ...any) *Error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, a...)}
}

// ErrNotConnected is returned by Link.Send while no stream is open.
var ErrNotConnected = errors.New("transport: not connected")

// ErrClosed is returned by a Stream after Close.
var ErrClosed = errors.New("transport: stream closed")

// IsPermanent reports a server rejection that reconnecting cannot fix.
func IsPermanent(err error) bool {
	var e *Error
	return errors.As(err, &e) && e.Permanent()
}
