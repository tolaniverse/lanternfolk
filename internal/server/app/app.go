package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/argon2"

	"github.com/tolaniverse/lanternfolk/internal/core/ids"
	"github.com/tolaniverse/lanternfolk/internal/core/workspace"
	"github.com/tolaniverse/lanternfolk/protocol"
)

// RecentPerChannel is how many messages per channel a snapshot carries.
const RecentPerChannel = 50

// SessionTTL is how long a sign-in lasts.
const SessionTTL = 30 * 24 * time.Hour

// App holds the services. Construct it with New.
type App struct {
	store Store
	wake  Waker
	now   func() time.Time
	hash  workspace.Hasher
	bus   *Bus
}

// New wires the services. wake may be nil.
func New(store Store, wake Waker) *App {
	if wake == nil {
		wake = func() {}
	}
	a := &App{store: store, wake: wake, now: time.Now, hash: Argon2id{}}
	a.bus = newBus(a)
	return a
}

// Argon2id hashes gate answers (OWASP baseline: 19 MiB, 2 passes, 1 lane).
type Argon2id struct{}

func (Argon2id) Hash(answer string, salt []byte) []byte {
	return argon2.IDKey([]byte(answer), salt, 2, 19*1024, 1, 32)
}

func newSalt() []byte {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return b
}

func hashToken(token string) []byte {
	s := sha256.Sum256([]byte(token))
	return s[:]
}

// ---- auth ----

// AuthDev signs in by handle, creating the user on first use.
func (a *App) AuthDev(ctx context.Context, handle string) (protocol.AuthResponse, error) {
	handle = strings.ToLower(strings.TrimSpace(handle))
	if !protocol.ValidHandle(handle) {
		return protocol.AuthResponse{}, fmt.Errorf("%w: handle must be 2–24 of a-z, 0-9, _", protocol.ErrInvalidPayload)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return protocol.AuthResponse{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	var user User
	err := a.store.Tx(ctx, func(tx Tx) error {
		u, err := tx.UserByHandle(handle)
		if errors.Is(err, ErrNotFound) {
			u = User{ID: ids.NewV7().String(), Handle: handle}
			err = tx.CreateUser(u, a.now())
		}
		if err != nil {
			return err
		}
		user = u
		return tx.CreateSession(hashToken(token), u.ID, a.now().Add(SessionTTL))
	})
	if err != nil {
		return protocol.AuthResponse{}, err
	}
	return protocol.AuthResponse{Token: token, UserID: user.ID, Handle: user.Handle}, nil
}

// Authenticate resolves a bearer token.
func (a *App) Authenticate(ctx context.Context, token string) (User, error) {
	if token == "" {
		return User{}, ErrUnauthorized
	}
	var u User
	err := a.store.Tx(ctx, func(tx Tx) (err error) {
		u, err = tx.UserBySession(hashToken(token), a.now())
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return User{}, ErrUnauthorized
	}
	return u, err
}

// ---- workspaces ----

func view(w Workspace) protocol.Workspace {
	return protocol.Workspace{ID: w.ID, Name: w.Name, Slug: w.Slug}
}

// CreateWorkspace makes a workspace with #general; the caller becomes owner.
func (a *App) CreateWorkspace(ctx context.Context, actor User, req protocol.CreateWorkspace) (protocol.Workspace, error) {
	req.Name = strings.TrimSpace(req.Name)
	if err := req.Validate(); err != nil {
		return protocol.Workspace{}, err
	}
	w := Workspace{ID: ids.NewV7().String(), Name: req.Name, Slug: req.Slug}
	gates := make([]workspace.Gate, len(req.Gates))
	for i, g := range req.Gates {
		salt := newSalt()
		gates[i] = workspace.Gate{Kind: string(g.Kind), Prompt: strings.TrimSpace(g.Prompt), Salt: salt,
			Hash: a.hash.Hash(protocol.NormalizeAnswer(g.Answer), salt)}
	}
	general := protocol.Channel{ID: ids.NewV7().String(), Name: "general"}
	err := a.store.Tx(ctx, func(tx Tx) error {
		now := a.now()
		if err := tx.CreateWorkspace(w, now); err != nil {
			return err
		}
		if err := tx.AddGates(w.ID, gates); err != nil {
			return err
		}
		if err := tx.AddMember(w.ID, actor.ID, workspace.Owner, now); err != nil {
			return err
		}
		if err := tx.CreateChannel(w.ID, general); err != nil {
			return err
		}
		if _, err := tx.Append(w.ID, protocol.TypeChannelCreated, protocol.ChannelCreated{Channel: general}); err != nil {
			return err
		}
		_, err := tx.Append(w.ID, protocol.TypeMemberJoined, protocol.MemberJoined{
			Member: protocol.Member{UserID: actor.ID, Handle: actor.Handle, Role: protocol.RoleOwner}})
		return err
	})
	if err != nil {
		return protocol.Workspace{}, err
	}
	a.wake()
	return view(w), nil
}

// Gates lists a workspace's gate prompts, in order.
func (a *App) Gates(ctx context.Context, slug string) ([]protocol.GatePrompt, error) {
	var out []protocol.GatePrompt
	err := a.store.Tx(ctx, func(tx Tx) error {
		w, err := tx.WorkspaceBySlug(slug)
		if err != nil {
			return err
		}
		gates, err := tx.Gates(w.ID)
		if err != nil {
			return err
		}
		out = make([]protocol.GatePrompt, len(gates))
		for i, g := range gates {
			out[i] = protocol.GatePrompt{Kind: protocol.GateKind(g.Kind), Prompt: g.Prompt}
		}
		return nil
	})
	return out, err
}

// Join answers a workspace's gates. Joining again is a no-op that returns
// the workspace.
func (a *App) Join(ctx context.Context, actor User, slug string, req protocol.JoinWorkspace) (protocol.Workspace, error) {
	answers := make([]string, len(req.Answers))
	for i, s := range req.Answers {
		answers[i] = protocol.NormalizeAnswer(s)
	}
	var w Workspace
	joined := false
	err := a.store.Tx(ctx, func(tx Tx) error {
		var err error
		if w, err = tx.WorkspaceBySlug(slug); err != nil {
			return err
		}
		if _, err := tx.Role(w.ID, actor.ID); err == nil {
			return nil // already a member
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		gates, err := tx.Gates(w.ID)
		if err != nil {
			return err
		}
		if err := workspace.CheckGates(gates, answers, a.hash); err != nil {
			return err
		}
		if err := tx.AddMember(w.ID, actor.ID, workspace.Member, a.now()); err != nil {
			return err
		}
		joined = true
		_, err = tx.Append(w.ID, protocol.TypeMemberJoined, protocol.MemberJoined{
			Member: protocol.Member{UserID: actor.ID, Handle: actor.Handle, Role: protocol.RoleMember}})
		return err
	})
	if err != nil {
		return protocol.Workspace{}, err
	}
	if joined {
		a.wake()
	}
	return view(w), nil
}

// Membership returns the workspace and the actor's role, or ErrForbidden.
func (a *App) Membership(ctx context.Context, actor User, workspaceID string) (Workspace, workspace.Role, error) {
	var w Workspace
	var role workspace.Role
	err := a.store.Tx(ctx, func(tx Tx) (err error) {
		if w, err = tx.WorkspaceByID(workspaceID); err != nil {
			return err
		}
		role, err = tx.Role(workspaceID, actor.ID)
		return err
	})
	if errors.Is(err, ErrNotFound) {
		return Workspace{}, "", ErrForbidden // don't reveal which workspaces exist
	}
	return w, role, err
}

// Snapshot is the client-visible state of a workspace (Online is filled in
// by the gateway from the hub).
func (a *App) Snapshot(ctx context.Context, workspaceID string) (protocol.Snapshot, error) {
	var s protocol.Snapshot
	err := a.store.Tx(ctx, func(tx Tx) error {
		w, err := tx.WorkspaceByID(workspaceID)
		if err != nil {
			return err
		}
		s.Seq, s.Workspace = w.Seq, view(w)
		if s.Members, err = tx.Members(workspaceID); err != nil {
			return err
		}
		if s.Channels, err = tx.Channels(workspaceID); err != nil {
			return err
		}
		s.Messages, err = tx.RecentMessages(workspaceID, RecentPerChannel)
		return err
	})
	return s, err
}

// EventsSince returns the durable events after seq, oldest first.
func (a *App) EventsSince(ctx context.Context, workspaceID string, since uint64, limit int) ([]Event, error) {
	var evs []Event
	err := a.store.Tx(ctx, func(tx Tx) (err error) {
		evs, err = tx.EventsSince(workspaceID, since, limit)
		return err
	})
	return evs, err
}

// Dispatch runs a WebSocket command through the bus and returns its ack.
func (a *App) Dispatch(ctx context.Context, actor User, env protocol.Envelope) protocol.Envelope {
	return a.bus.dispatch(ctx, actor, env)
}
