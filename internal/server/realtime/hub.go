// Package realtime fans committed workspace events and ephemeral presence to
// connected clients. A mutex protects each workspace's subscriber registry;
// no network writes occur while it is held.
package realtime

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"time"

	"github.com/tolaniverse/lanternfolk/internal/server/app"
	"github.com/tolaniverse/lanternfolk/protocol"
)

type Hub struct {
	mu    sync.Mutex
	rooms map[string]*room
}

type room struct {
	clients map[chan protocol.Envelope]string
	counts  map[string]int
}

func New() *Hub { return &Hub{rooms: make(map[string]*room)} }

func (h *Hub) room(id string) *room {
	r := h.rooms[id]
	if r == nil {
		r = &room{clients: make(map[chan protocol.Envelope]string), counts: make(map[string]int)}
		h.rooms[id] = r
	}
	return r
}

// Subscribe registers before the caller reads its snapshot/backlog, so a
// concurrent commit cannot fall into the gap. Cancel must be called once.
func (h *Hub) Subscribe(workspaceID, userID string) (<-chan protocol.Envelope, func()) {
	h.mu.Lock()
	r := h.room(workspaceID)
	ch := make(chan protocol.Envelope, 256)
	r.clients[ch] = userID
	r.counts[userID]++
	if r.counts[userID] == 1 {
		h.broadcast(r, workspaceID, protocol.Presence{Changes: []protocol.PresenceChange{{UserID: userID, Status: protocol.Online}}})
	}
	h.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			delete(r.clients, ch)
			close(ch)
			r.counts[userID]--
			if r.counts[userID] == 0 {
				delete(r.counts, userID)
				h.broadcast(r, workspaceID, protocol.Presence{Changes: []protocol.PresenceChange{{UserID: userID, Status: protocol.Offline}}})
			}
			if len(r.clients) == 0 {
				delete(h.rooms, workspaceID)
			}
		})
	}
}

func (h *Hub) Online(workspaceID string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[workspaceID]
	if r == nil {
		return []string{}
	}
	ids := make([]string, 0, len(r.counts))
	for id := range r.counts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (h *Hub) broadcast(r *room, id string, p protocol.Presence) {
	b, _ := json.Marshal(p)
	e := protocol.Envelope{V: protocol.Version, Kind: protocol.KindPresence, WorkspaceID: id, Payload: b}
	for ch := range r.clients {
		select {
		case ch <- e:
		default: /* slow clients resync on reconnect */
		}
	}
}

// Publish sends one committed event. Slow consumers receive a resync signal
// where possible; the gateway disconnects them when the buffer stays full.
func (h *Hub) Publish(e app.Event) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r := h.rooms[e.WorkspaceID]
	if r == nil {
		return
	}
	f := protocol.Envelope{V: protocol.Version, Kind: protocol.KindEvent, WorkspaceID: e.WorkspaceID, Seq: e.Seq, Type: e.Type, Payload: e.Payload}
	for ch := range r.clients {
		select {
		case ch <- f:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- protocol.Envelope{V: protocol.Version, Kind: protocol.KindResync, WorkspaceID: e.WorkspaceID}:
			default:
			}
		}
	}
}

// Relay drains committed outbox rows. Replaying a row after a crash is safe:
// clients discard events at or below their last sequence.
func Relay(ctx context.Context, store app.Store, publish func(app.Event), wake <-chan struct{}) error {
	for {
		var batch []app.Event
		err := store.Tx(ctx, func(tx app.Tx) (err error) { batch, err = tx.Unpublished(256); return err })
		if err != nil {
			return err
		}
		if len(batch) > 0 {
			for _, e := range batch {
				publish(e)
			}
			if err := store.Tx(ctx, func(tx app.Tx) error { return tx.MarkPublished(batch) }); err != nil {
				return err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil
		case <-wake:
		case <-time.After(time.Second):
		}
	}
}
