// Package app is lanternfolkd's application layer: the services behind every
// HTTP endpoint and WebSocket command. It declares the ports it needs (Store,
// Tx, Publisher); adapters implement them and cmd/lanternfolkd wires them.
package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/tolaniverse/lanternfolk/internal/core/workspace"
	"github.com/tolaniverse/lanternfolk/protocol"
)

// Errors adapters and services return. The gateway maps them to HTTP codes
// and ack error codes.
var (
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("already exists")
	ErrForbidden    = errors.New("forbidden")
	ErrUnauthorized = errors.New("unauthorized")
)

type User struct {
	ID     string
	Handle string
}

type Workspace struct {
	ID   string
	Name string
	Slug string
	Seq  uint64
}

// Event is one outbox row: a durable event with its per-workspace seq.
type Event struct {
	WorkspaceID string
	Seq         uint64
	Type        string
	Payload     json.RawMessage
}

// Store runs a unit of work. fn's writes, including Append, commit together
// or not at all.
type Store interface {
	Tx(ctx context.Context, fn func(Tx) error) error
}

// Tx is everything a unit of work can read and write.
type Tx interface {
	CreateUser(u User, now time.Time) error // ErrConflict on a taken handle
	UserByHandle(handle string) (User, error)
	CreateSession(tokenHash []byte, userID string, expires time.Time) error
	UserBySession(tokenHash []byte, now time.Time) (User, error)

	CreateWorkspace(w Workspace, now time.Time) error // ErrConflict on a taken slug
	WorkspaceBySlug(slug string) (Workspace, error)
	WorkspaceByID(id string) (Workspace, error)
	AddGates(workspaceID string, gates []workspace.Gate) error
	Gates(workspaceID string) ([]workspace.Gate, error)

	AddMember(workspaceID, userID string, role workspace.Role, now time.Time) error
	Role(workspaceID, userID string) (workspace.Role, error) // ErrNotFound if not a member
	Members(workspaceID string) ([]protocol.Member, error)

	CreateChannel(workspaceID string, c protocol.Channel) error
	ChannelExists(workspaceID, channelID string) (bool, error)
	Channels(workspaceID string) ([]protocol.Channel, error)

	// MessageByClientCmd finds an author's earlier message with the same
	// client_cmd_id (the idempotency key) and the seq of its event.
	MessageByClientCmd(workspaceID, authorID, clientCmdID string) (protocol.Message, uint64, error)
	// InsertMessage stores m with the seq of its MessagePosted event.
	InsertMessage(workspaceID string, m protocol.Message, seq uint64) error
	RecentMessages(workspaceID string, perChannel int) ([]protocol.Message, error)

	// Append adds an event to the outbox with the workspace's next seq.
	Append(workspaceID, typ string, payload any) (uint64, error)
	EventsSince(workspaceID string, since uint64, limit int) ([]Event, error)
	Unpublished(limit int) ([]Event, error)
	MarkPublished(events []Event) error
}

// Waker is told after a commit that appended events, so the outbox relay
// publishes them without waiting for its poll.
type Waker func()
