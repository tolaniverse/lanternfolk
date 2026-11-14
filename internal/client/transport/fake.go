package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tolaniverse/lanternfolk/protocol"
)

// FakeServer speaks the same API as lanternfolkd without external I/O.
// It supports demo mode and deterministic headless client tests.
type FakeServer struct {
	mu         sync.Mutex
	users      map[string]protocol.AuthResponse
	tokens     map[string]string
	workspaces map[string]*fakeWorkspace
}

type fakeWorkspace struct {
	view     protocol.Workspace
	gates    []protocol.GateSpec
	members  []protocol.Member
	channels []protocol.Channel
	messages []protocol.Message
	events   []protocol.Envelope
	seq      uint64
	streams  map[*fakeStream]string
}

func NewFake() *FakeServer {
	return &FakeServer{users: map[string]protocol.AuthResponse{}, tokens: map[string]string{}, workspaces: map[string]*fakeWorkspace{}}
}

func randomID() string { var b [16]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }

func (f *FakeServer) user(token string) (protocol.AuthResponse, error) {
	h := f.tokens[token]
	if h == "" {
		return protocol.AuthResponse{}, errorFor(http.StatusUnauthorized, CodeUnauthorized, "sign in first")
	}
	return f.users[h], nil
}

func (f *FakeServer) AuthDev(_ context.Context, handle string) (protocol.AuthResponse, error) {
	if !protocol.ValidHandle(handle) {
		return protocol.AuthResponse{}, errorFor(400, CodeInvalid, "invalid handle")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u := f.users[handle]
	if u.UserID == "" {
		u.UserID = randomID()
		u.Handle = handle
	}
	u.Token = randomID()
	f.users[handle] = u
	f.tokens[u.Token] = handle
	return u, nil
}

func (f *FakeServer) CreateWorkspace(_ context.Context, token string, req protocol.CreateWorkspace) (protocol.Workspace, error) {
	if err := req.Validate(); err != nil {
		return protocol.Workspace{}, errorFor(400, CodeInvalid, "%v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	u, err := f.user(token)
	if err != nil {
		return protocol.Workspace{}, err
	}
	if f.workspaces[req.Slug] != nil {
		return protocol.Workspace{}, errorFor(409, CodeConflict, "slug taken")
	}
	w := protocol.Workspace{ID: randomID(), Name: req.Name, Slug: req.Slug}
	fw := &fakeWorkspace{view: w, gates: req.Gates, members: []protocol.Member{{UserID: u.UserID, Handle: u.Handle, Role: protocol.RoleOwner}}, channels: []protocol.Channel{{ID: randomID(), Name: "general"}}, streams: map[*fakeStream]string{}}
	fw.append(protocol.TypeChannelCreated, protocol.ChannelCreated{Channel: fw.channels[0]})
	fw.append(protocol.TypeMemberJoined, protocol.MemberJoined{Member: fw.members[0]})
	f.workspaces[req.Slug] = fw
	return w, nil
}

func (f *FakeServer) Gates(_ context.Context, _ string, slug string) ([]protocol.GatePrompt, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w := f.workspaces[slug]
	if w == nil {
		return nil, errorFor(404, CodeNotFound, "workspace not found")
	}
	out := make([]protocol.GatePrompt, len(w.gates))
	for i, g := range w.gates {
		out[i] = protocol.GatePrompt{Kind: g.Kind, Prompt: g.Prompt}
	}
	return out, nil
}

func (f *FakeServer) Join(_ context.Context, token, slug string, req protocol.JoinWorkspace) (protocol.Workspace, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, err := f.user(token)
	if err != nil {
		return protocol.Workspace{}, err
	}
	w := f.workspaces[slug]
	if w == nil {
		return protocol.Workspace{}, errorFor(404, CodeNotFound, "workspace not found")
	}
	for _, m := range w.members {
		if m.UserID == u.UserID {
			return w.view, nil
		}
	}
	if len(req.Answers) != len(w.gates) {
		return protocol.Workspace{}, errorFor(400, CodeInvalid, "answer every gate")
	}
	for i, g := range w.gates {
		if protocol.NormalizeAnswer(req.Answers[i]) != protocol.NormalizeAnswer(g.Answer) {
			return protocol.Workspace{}, errorFor(400, CodeInvalid, "the gate keeper is not convinced")
		}
	}
	m := protocol.Member{UserID: u.UserID, Handle: u.Handle, Role: protocol.RoleMember}
	w.members = append(w.members, m)
	w.append(protocol.TypeMemberJoined, protocol.MemberJoined{Member: m})
	return w.view, nil
}

func (w *fakeWorkspace) append(typ string, payload any) protocol.Envelope {
	w.seq++
	b, _ := json.Marshal(payload)
	e := protocol.Envelope{V: protocol.Version, Kind: protocol.KindEvent, WorkspaceID: w.view.ID, Seq: w.seq, Type: typ, Payload: b}
	w.events = append(w.events, e)
	for s := range w.streams {
		s.push(e)
	}
	return e
}

func (f *FakeServer) Dial(_ context.Context, token, workspaceID string, since uint64) (Stream, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, err := f.user(token)
	if err != nil {
		return nil, err
	}
	var w *fakeWorkspace
	for _, v := range f.workspaces {
		if v.view.ID == workspaceID {
			w = v
			break
		}
	}
	if w == nil {
		return nil, errorFor(404, CodeNotFound, "workspace not found")
	}
	member := false
	for _, m := range w.members {
		if m.UserID == u.UserID {
			member = true
			break
		}
	}
	if !member {
		return nil, errorFor(403, CodeForbidden, "not a member")
	}
	s := &fakeStream{server: f, ws: w, user: u, frames: make(chan protocol.Envelope, 256), done: make(chan struct{})}
	w.streams[s] = u.UserID
	if since == 0 || since > w.seq {
		online := make([]string, 0, len(w.streams))
		seen := map[string]bool{}
		for _, id := range w.streams {
			if !seen[id] {
				online = append(online, id)
				seen[id] = true
			}
		}
		sort.Strings(online)
		p, _ := json.Marshal(protocol.Snapshot{Seq: w.seq, Workspace: w.view, Members: append([]protocol.Member{}, w.members...), Channels: append([]protocol.Channel{}, w.channels...), Messages: append([]protocol.Message{}, w.messages...), Online: online})
		s.push(protocol.Envelope{V: protocol.Version, Kind: protocol.KindSnapshot, WorkspaceID: workspaceID, Seq: w.seq, Payload: p})
	} else {
		for _, e := range w.events {
			if e.Seq > since {
				s.push(e)
			}
		}
	}
	p, _ := json.Marshal(protocol.Presence{Changes: []protocol.PresenceChange{{UserID: u.UserID, Status: protocol.Online}}})
	for peer := range w.streams {
		peer.push(protocol.Envelope{V: protocol.Version, Kind: protocol.KindPresence, WorkspaceID: workspaceID, Payload: p})
	}
	return s, nil
}

type fakeStream struct {
	server *FakeServer
	ws     *fakeWorkspace
	user   protocol.AuthResponse
	frames chan protocol.Envelope
	done   chan struct{}
	closed bool
}

func (s *fakeStream) push(e protocol.Envelope) {
	select {
	case s.frames <- e:
	default:
	}
}
func (s *fakeStream) Recv(ctx context.Context) (protocol.Envelope, error) {
	select {
	case <-ctx.Done():
		return protocol.Envelope{}, ctx.Err()
	case <-s.done:
		return protocol.Envelope{}, ErrClosed
	case e := <-s.frames:
		return e, nil
	}
}
func (s *fakeStream) Send(ctx context.Context, e protocol.Envelope) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.server.mu.Lock()
	defer s.server.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if e.Kind != protocol.KindCommand || e.Type != protocol.TypeSendMessage || e.WorkspaceID != s.ws.view.ID {
		return errorFor(400, CodeInvalid, "unsupported command")
	}
	ack := protocol.Envelope{V: protocol.Version, Kind: protocol.KindAck, Type: e.Type, ClientCmdID: e.ClientCmdID, WorkspaceID: e.WorkspaceID}
	p, err := protocol.DecodePayload[protocol.SendMessage](e)
	if err == nil {
		err = p.Validate()
	}
	if err == nil && p.ChannelID != s.ws.channels[0].ID {
		err = protocol.ErrInvalidPayload
	}
	if err != nil {
		ack.Error = &protocol.Error{Code: CodeInvalid, Message: err.Error()}
		s.push(ack)
		return nil
	}
	for _, m := range s.ws.messages {
		if m.AuthorID == s.user.UserID && m.ClientCmdID == e.ClientCmdID {
			for _, event := range s.ws.events {
				if event.Type == protocol.TypeMessagePosted && strings.Contains(string(event.Payload), m.ID) {
					ack.Seq = event.Seq
					break
				}
			}
			s.push(ack)
			return nil
		}
	}
	m := protocol.Message{ID: randomID(), ChannelID: p.ChannelID, AuthorID: s.user.UserID, Body: strings.TrimSpace(p.Body), ClientCmdID: e.ClientCmdID, CreatedAt: time.Now().UTC()}
	s.ws.messages = append(s.ws.messages, m)
	event := s.ws.append(protocol.TypeMessagePosted, protocol.MessagePosted{Message: m})
	ack.Seq = event.Seq
	s.push(ack)
	return nil
}
func (s *fakeStream) Close() error {
	s.server.mu.Lock()
	defer s.server.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	delete(s.ws.streams, s)
	close(s.done)
	for _, id := range s.ws.streams {
		if id == s.user.UserID {
			return nil
		}
	}
	p, _ := json.Marshal(protocol.Presence{Changes: []protocol.PresenceChange{{UserID: s.user.UserID, Status: protocol.Offline}}})
	for peer := range s.ws.streams {
		peer.push(protocol.Envelope{V: protocol.Version, Kind: protocol.KindPresence, WorkspaceID: s.ws.view.ID, Payload: p})
	}
	return nil
}
