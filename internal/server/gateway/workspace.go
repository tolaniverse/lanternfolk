package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/tolaniverse/lanternfolk/internal/core/workspace"
	"github.com/tolaniverse/lanternfolk/internal/server/app"
	"github.com/tolaniverse/lanternfolk/protocol"
)

const maxBody = 32 << 10

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, protocol.ErrInvalidPayload), errors.Is(err, workspace.ErrGateFailed):
		status = http.StatusBadRequest
	case errors.Is(err, app.ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, app.ErrConflict):
		status = http.StatusConflict
	case errors.Is(err, app.ErrForbidden):
		status = http.StatusForbidden
	case errors.Is(err, app.ErrUnauthorized):
		status = http.StatusUnauthorized
	}
	message := http.StatusText(status)
	if status < 500 {
		message = err.Error()
	}
	writeJSON(w, status, map[string]string{"error": message})
}

func body(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, maxBody+1))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return protocol.ErrInvalidPayload
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return protocol.ErrInvalidPayload
	}
	return nil
}

func (g *Gateway) user(r *http.Request) (app.User, error) {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return app.User{}, app.ErrUnauthorized
	}
	return g.app.Authenticate(r.Context(), strings.TrimPrefix(header, "Bearer "))
}

func (g *Gateway) authDev(w http.ResponseWriter, r *http.Request) {
	if !g.devAuth {
		http.NotFound(w, r)
		return
	}
	var req protocol.AuthDevRequest
	if err := body(r, &req); err != nil {
		apiError(w, err)
		return
	}
	resp, err := g.app.AuthDev(r.Context(), req.Handle)
	if err != nil {
		apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

func (g *Gateway) createWorkspace(w http.ResponseWriter, r *http.Request) {
	u, err := g.user(r)
	if err != nil {
		apiError(w, err)
		return
	}
	var req protocol.CreateWorkspace
	if err := body(r, &req); err != nil {
		apiError(w, err)
		return
	}
	ws, err := g.app.CreateWorkspace(r.Context(), u, req)
	if err != nil {
		apiError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, ws)
}

func (g *Gateway) gates(w http.ResponseWriter, r *http.Request) {
	// Gate prompts are public so a prospective member can answer them.
	list, err := g.app.Gates(r.Context(), r.PathValue("slug"))
	if err != nil {
		apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (g *Gateway) join(w http.ResponseWriter, r *http.Request) {
	u, err := g.user(r)
	if err != nil {
		apiError(w, err)
		return
	}
	var req protocol.JoinWorkspace
	if err := body(r, &req); err != nil {
		apiError(w, err)
		return
	}
	ws, err := g.app.Join(r.Context(), u, r.PathValue("slug"), req)
	if err != nil {
		apiError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ws)
}

func (g *Gateway) websocket(w http.ResponseWriter, r *http.Request) {
	u, err := g.user(r)
	if err != nil {
		apiError(w, err)
		return
	}
	id := r.URL.Query().Get("workspace")
	if id == "" {
		apiError(w, protocol.ErrInvalidPayload)
		return
	}
	if _, _, err := g.app.Membership(r.Context(), u, id); err != nil {
		apiError(w, err)
		return
	}
	since, err := strconv.ParseUint(r.URL.Query().Get("since"), 10, 64)
	if err != nil {
		apiError(w, protocol.ErrInvalidPayload)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	frames, unsubscribe := g.hub.Subscribe(id, u.ID)
	defer unsubscribe()
	// A single writer owns the connection. It subscribes before reading the DB;
	// queued events at or below the read cursor are discarded below.
	snap, err := g.app.Snapshot(ctx, id)
	if err != nil {
		return
	}
	var start uint64
	if since == 0 || since > snap.Seq {
		snap.Online = g.hub.Online(id)
		p, _ := json.Marshal(snap)
		if !send(ctx, conn, protocol.Envelope{Kind: protocol.KindSnapshot, WorkspaceID: id, Seq: snap.Seq, Payload: p}) {
			return
		}
		start = snap.Seq
	} else {
		events, err := g.app.EventsSince(ctx, id, since, 1001)
		if err != nil {
			return
		}
		if len(events) > 1000 || (len(events) > 0 && events[0].Seq != since+1) {
			snap.Online = g.hub.Online(id)
			p, _ := json.Marshal(snap)
			if !send(ctx, conn, protocol.Envelope{Kind: protocol.KindSnapshot, WorkspaceID: id, Seq: snap.Seq, Payload: p}) {
				return
			}
			start = snap.Seq
		} else {
			for _, e := range events {
				if !send(ctx, conn, protocol.Envelope{Kind: protocol.KindEvent, WorkspaceID: id, Seq: e.Seq, Type: e.Type, Payload: e.Payload}) {
					return
				}
				start = e.Seq
			}
			if start < snap.Seq {
				start = snap.Seq
			}
		}
	}
	// Presence also marks the end of a resume replay for the client reducer.
	p, _ := json.Marshal(protocol.Presence{Changes: onlineChanges(g.hub.Online(id))})
	if !send(ctx, conn, protocol.Envelope{Kind: protocol.KindPresence, WorkspaceID: id, Payload: p}) {
		return
	}
	ackCh := make(chan protocol.Envelope, 16)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			_, b, err := conn.Read(ctx)
			if err != nil {
				return
			}
			e, err := protocol.Decode(b)
			if err != nil || e.Kind != protocol.KindCommand || e.WorkspaceID != id {
				return
			}
			ack := g.app.Dispatch(ctx, u, e)
			select {
			case ackCh <- ack:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); <-done }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-done:
			return
		case ack := <-ackCh:
			if !send(ctx, conn, ack) {
				return
			}
		case e := <-frames:
			if e.Kind == protocol.KindEvent && e.Seq <= start {
				continue
			}
			if e.Kind == protocol.KindEvent {
				start = e.Seq
			}
			if !send(ctx, conn, e) || e.Kind == protocol.KindResync {
				return
			}
		}
	}
}

func onlineChanges(ids []string) []protocol.PresenceChange {
	out := make([]protocol.PresenceChange, 0, len(ids))
	for _, id := range ids {
		out = append(out, protocol.PresenceChange{UserID: id, Status: protocol.Online})
	}
	return out
}

func send(ctx context.Context, c *websocket.Conn, e protocol.Envelope) bool {
	b, err := protocol.Encode(e)
	if err != nil {
		return false
	}
	writeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return c.Write(writeCtx, websocket.MessageText, b) == nil
}
