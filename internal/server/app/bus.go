package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/tolaniverse/lanternfolk/internal/core/ids"
	"github.com/tolaniverse/lanternfolk/internal/core/workspace"
	"github.com/tolaniverse/lanternfolk/protocol"
)

// Cmd is one command flowing through the middleware chain.
type Cmd struct {
	Actor User
	Env   protocol.Envelope
	Role  workspace.Role // set by the authz middleware
}

// Handler runs a command and returns the seq of the event it appended
// (0 when it appended none).
type Handler func(ctx context.Context, c *Cmd) (uint64, error)

// Middleware wraps a Handler: Chain of Responsibility / Decorator.
type Middleware func(Handler) Handler

// Bus routes commands by type through one shared middleware chain, so every
// command is authorized, validated, and recovered the same way.
type Bus struct {
	app      *App
	handlers map[string]Handler
}

func newBus(a *App) *Bus {
	b := &Bus{app: a, handlers: map[string]Handler{}}
	chain := []Middleware{b.recoverMW, b.authzMW}
	b.handle(protocol.TypeSendMessage, b.sendMessage, chain...)
	return b
}

func (b *Bus) handle(typ string, h Handler, mw ...Middleware) {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	b.handlers[typ] = h
}

func (b *Bus) dispatch(ctx context.Context, actor User, env protocol.Envelope) protocol.Envelope {
	ack := protocol.Envelope{V: protocol.Version, Kind: protocol.KindAck, ClientCmdID: env.ClientCmdID,
		WorkspaceID: env.WorkspaceID, Type: env.Type}
	h, ok := b.handlers[env.Type]
	if !ok {
		ack.Error = &protocol.Error{Code: "unknown_command", Message: env.Type}
		return ack
	}
	seq, err := h(ctx, &Cmd{Actor: actor, Env: env})
	if err != nil {
		ack.Error = AckError(err)
		return ack
	}
	ack.Seq = seq
	return ack
}

// AckError maps an error to a wire error code.
func AckError(err error) *protocol.Error {
	switch {
	case errors.Is(err, protocol.ErrInvalidPayload):
		return &protocol.Error{Code: "invalid", Message: strings.TrimPrefix(err.Error(), "protocol: invalid payload: ")}
	case errors.Is(err, ErrForbidden):
		return &protocol.Error{Code: "forbidden"}
	case errors.Is(err, ErrNotFound):
		return &protocol.Error{Code: "not_found"}
	default:
		return &protocol.Error{Code: "unavailable"}
	}
}

func (b *Bus) recoverMW(next Handler) Handler {
	return func(ctx context.Context, c *Cmd) (seq uint64, err error) {
		defer func() {
			if r := recover(); r != nil {
				slog.ErrorContext(ctx, "command panicked", "type", c.Env.Type, "panic", fmt.Sprint(r))
				err = fmt.Errorf("internal error")
			}
		}()
		return next(ctx, c)
	}
}

func (b *Bus) authzMW(next Handler) Handler {
	return func(ctx context.Context, c *Cmd) (uint64, error) {
		_, role, err := b.app.Membership(ctx, c.Actor, c.Env.WorkspaceID)
		if err != nil {
			return 0, err
		}
		c.Role = role
		return next(ctx, c)
	}
}

func (b *Bus) sendMessage(ctx context.Context, c *Cmd) (uint64, error) {
	if !c.Role.CanPost() {
		return 0, ErrForbidden
	}
	p, err := protocol.DecodePayload[protocol.SendMessage](c.Env)
	if err != nil {
		return 0, err
	}
	if err := p.Validate(); err != nil {
		return 0, err
	}
	ws := c.Env.WorkspaceID
	var seq uint64
	err = b.app.store.Tx(ctx, func(tx Tx) error {
		ok, err := tx.ChannelExists(ws, p.ChannelID)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("%w: no such channel", protocol.ErrInvalidPayload)
		}
		if c.Env.ClientCmdID != "" {
			// A retry of a command already applied: ack it again with the
			// original event's seq and append nothing.
			_, s, err := tx.MessageByClientCmd(ws, c.Actor.ID, c.Env.ClientCmdID)
			if err == nil {
				seq = s
				return nil
			}
			if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		msg := protocol.Message{ID: ids.NewV7().String(), ChannelID: p.ChannelID, AuthorID: c.Actor.ID,
			Body: strings.TrimSpace(p.Body), CreatedAt: b.app.now().UTC().Truncate(time.Millisecond), ClientCmdID: c.Env.ClientCmdID}
		if seq, err = tx.Append(ws, protocol.TypeMessagePosted, protocol.MessagePosted{Message: msg}); err != nil {
			return err
		}
		return tx.InsertMessage(ws, msg, seq)
	})
	if err != nil {
		return 0, err
	}
	b.app.wake()
	return seq, nil
}
