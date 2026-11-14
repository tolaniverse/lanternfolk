// Package client orchestrates the transport and the pure reducer. One loop
// owns State; network goroutines only submit messages to that loop.
package client

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	rand2 "math/rand/v2"
	"sync"
	"time"

	"github.com/tolaniverse/lanternfolk/internal/client/core"
	"github.com/tolaniverse/lanternfolk/internal/client/transport"
	"github.com/tolaniverse/lanternfolk/protocol"
)

type Session struct {
	api    transport.API
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan func(*sessionState)
	done   chan struct{}
	mu     sync.Mutex // protects the stream pointer (the reducer state is loop-owned)
	stream transport.Stream
}

type sessionState struct {
	state core.State
	subs  map[uint64]func(core.State)
	next  uint64
}

func New(api transport.API) *Session {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{api: api, ctx: ctx, cancel: cancel, queue: make(chan func(*sessionState), 128), done: make(chan struct{})}
	go s.loop()
	return s
}

func (s *Session) loop() {
	defer close(s.done)
	st := sessionState{subs: map[uint64]func(core.State){}}
	for {
		select {
		case <-s.ctx.Done():
			return
		case fn := <-s.queue:
			fn(&st)
		}
	}
}

func (s *Session) post(fn func(*sessionState)) bool {
	select {
	case <-s.ctx.Done():
		return false
	case s.queue <- fn:
		return true
	}
}

func (s *Session) reduce(m core.Msg) {
	s.post(func(st *sessionState) {
		st.state = core.Reduce(st.state, m)
		for _, notify := range st.subs {
			notify(st.state)
		}
	})
}

// State returns a reducer snapshot. Its maps and slices must be treated as
// read-only; the reducer copies any collection before changing it.
func (s *Session) State() core.State {
	reply := make(chan core.State, 1)
	if !s.post(func(st *sessionState) { reply <- st.state }) {
		return core.State{}
	}
	return <-reply
}

// Subscribe notifies fn on the session loop. It must return promptly and
// must not call State or any other synchronous Session method.
func (s *Session) Subscribe(fn func(core.State)) (unsubscribe func()) {
	reply := make(chan uint64, 1)
	if !s.post(func(st *sessionState) { st.next++; st.subs[st.next] = fn; fn(st.state); reply <- st.next }) {
		return func() {}
	}
	id := <-reply
	return func() { s.post(func(st *sessionState) { delete(st.subs, id) }) }
}

func (s *Session) SignIn(ctx context.Context, handle string) (protocol.AuthResponse, error) {
	s.reduce(core.OpStarted{Op: core.OpSignIn})
	a, err := s.api.AuthDev(ctx, handle)
	if err != nil {
		s.reduce(core.OpFailed{Op: core.OpSignIn, Err: err.Error()})
		return a, err
	}
	s.reduce(core.SignedIn{Auth: a})
	return a, nil
}
func (s *Session) Create(ctx context.Context, req protocol.CreateWorkspace) (protocol.Workspace, error) {
	s.reduce(core.OpStarted{Op: core.OpCreate})
	w, err := s.api.CreateWorkspace(ctx, s.State().Auth.Token, req)
	if err != nil {
		s.reduce(core.OpFailed{Op: core.OpCreate, Err: err.Error()})
		return w, err
	}
	s.enter(w)
	return w, nil
}
func (s *Session) Gates(ctx context.Context, slug string) ([]protocol.GatePrompt, error) {
	s.reduce(core.OpStarted{Op: core.OpGates})
	g, err := s.api.Gates(ctx, s.State().Auth.Token, slug)
	if err != nil {
		s.reduce(core.OpFailed{Op: core.OpGates, Err: err.Error()})
		return nil, err
	}
	s.reduce(core.GatesLoaded{Slug: slug, Prompts: g})
	return g, nil
}
func (s *Session) Join(ctx context.Context, slug string, answers []string) (protocol.Workspace, error) {
	s.reduce(core.OpStarted{Op: core.OpJoin})
	w, err := s.api.Join(ctx, s.State().Auth.Token, slug, protocol.JoinWorkspace{Answers: answers})
	if err != nil {
		s.reduce(core.OpFailed{Op: core.OpJoin, Err: err.Error()})
		return w, err
	}
	s.enter(w)
	return w, nil
}

func (s *Session) enter(w protocol.Workspace) {
	s.reduce(core.WorkspaceEntered{Workspace: w})
	go s.connect(w.ID)
}

func (s *Session) connect(workspaceID string) {
	backoff := 200 * time.Millisecond
	for s.ctx.Err() == nil {
		s.reduce(core.ConnChanged{To: core.Connecting})
		st := s.State()
		stream, err := s.api.Dial(s.ctx, st.Auth.Token, workspaceID, st.Since())
		if err != nil {
			if errors.Is(err, context.Canceled) {
				return
			}
			if transport.IsPermanent(err) {
				s.reduce(core.ConnChanged{To: core.Disconnected, Detail: err.Error()})
				return
			}
			s.reduce(core.ConnChanged{To: core.Backoff, Detail: err.Error()})
		} else {
			s.mu.Lock()
			s.stream = stream
			s.mu.Unlock()
			s.reduce(core.ConnChanged{To: core.Resyncing})
			backoff = 200 * time.Millisecond
			replayed := false
			for s.ctx.Err() == nil {
				frame, e := stream.Recv(s.ctx)
				if e != nil {
					err = e
					break
				}
				s.reduce(core.FrameReceived{Frame: frame})
				if frame.Kind == protocol.KindResync {
					err = errors.New("server requested resync")
					break
				}
				if !replayed && s.State().Conn == core.Live {
					replayed = true
					// The server may have applied a command before the socket
					// dropped. Retrying with the same ID is idempotent.
					for _, pending := range s.State().PendingInOrder() {
						if pending.Status == core.Failed {
							continue
						}
						cmd, err := protocol.NewCommand(protocol.TypeSendMessage, pending.ClientCmdID, workspaceID,
							protocol.SendMessage{ChannelID: pending.ChannelID, Body: pending.Body})
						if err == nil {
							if err = stream.Send(s.ctx, cmd); err != nil {
								break
							}
						}
					}
				}
			}
			s.mu.Lock()
			if s.stream == stream {
				s.stream = nil
			}
			s.mu.Unlock()
			_ = stream.Close()
			if s.ctx.Err() != nil {
				return
			}
			s.reduce(core.ConnChanged{To: core.Backoff, Detail: err.Error()})
		}
		select {
		case <-s.ctx.Done():
			return
		case <-time.After(backoff + time.Duration(rand2.Int64N(int64(backoff/4+1)))):
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

// Send queues an optimistic message and sends its idempotent command. A
// disconnected send remains pending and is retried after reconnection.
func (s *Session) Send(ctx context.Context, body string) error {
	st := s.State()
	channel := st.DefaultChannel()
	if channel == "" {
		return transport.ErrNotConnected
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	cmdID := hex.EncodeToString(id[:])
	s.reduce(core.SendRequested{ClientCmdID: cmdID, ChannelID: channel, Body: body, At: time.Now()})
	e, err := protocol.NewCommand(protocol.TypeSendMessage, cmdID, st.Workspace.ID, protocol.SendMessage{ChannelID: channel, Body: body})
	if err != nil {
		return err
	}
	s.mu.Lock()
	stream := s.stream
	s.mu.Unlock()
	if stream == nil {
		return transport.ErrNotConnected
	}
	return stream.Send(ctx, e)
}

func (s *Session) Close() {
	s.cancel()
	s.mu.Lock()
	if s.stream != nil {
		_ = s.stream.Close()
	}
	s.mu.Unlock()
	<-s.done
}
