package core

import (
	"maps"
	"slices"
	"time"

	"github.com/tolaniverse/lanternfolk/protocol"
)

// Msg is anything that can change State: a server frame, a local intent,
// a connection change, or the result of an HTTP operation.
type Msg interface{ isMsg() }

// FrameReceived is one frame from the WebSocket.
type FrameReceived struct{ Frame protocol.Envelope }

// ConnChanged asks the connection state machine to move to To.
type ConnChanged struct {
	To     ConnState
	Detail string
}

// CaughtUp says the replay after a reconnect is over (no snapshot was
// needed and the socket has been quiet), so the client is Live.
type CaughtUp struct{}

// OpStarted marks an HTTP operation in flight and clears the last notice.
type OpStarted struct{ Op Op }

// OpFailed ends an operation with an error for the user.
type OpFailed struct {
	Op  Op
	Err string
}

// SignedIn stores the bearer token.
type SignedIn struct{ Auth protocol.AuthResponse }

// GatesLoaded holds the prompts of a workspace the user wants to join.
type GatesLoaded struct {
	Slug    string
	Prompts []protocol.GatePrompt
}

// WorkspaceEntered switches to a workspace the user created or joined. Its
// data arrives with the first snapshot.
type WorkspaceEntered struct{ Workspace protocol.Workspace }

// SendRequested is the local intent to post a message. It shows at once as
// a Pending entry.
type SendRequested struct {
	ClientCmdID string
	ChannelID   string
	Body        string
	At          time.Time
}

func (FrameReceived) isMsg()    {}
func (ConnChanged) isMsg()      {}
func (CaughtUp) isMsg()         {}
func (OpStarted) isMsg()        {}
func (OpFailed) isMsg()         {}
func (SignedIn) isMsg()         {}
func (GatesLoaded) isMsg()      {}
func (WorkspaceEntered) isMsg() {}
func (SendRequested) isMsg()    {}

// Reduce returns the state after m. It never mutates s.
func Reduce(s State, m Msg) State {
	switch m := m.(type) {
	case FrameReceived:
		return reduceFrame(s, m.Frame)
	case ConnChanged:
		return reduceConn(s, m)
	case CaughtUp:
		if s.Conn == Resyncing && !s.NeedSnapshot {
			s.Conn, s.ConnDetail = Live, ""
		}
	case OpStarted:
		s.Busy, s.Notice = m.Op, Notice{}
	case OpFailed:
		s.Busy, s.Notice = "", Notice{Op: m.Op, Text: m.Err}
	case SignedIn:
		s.Busy, s.Auth = "", m.Auth
	case GatesLoaded:
		s.Busy = ""
		s.Gates = Gates{Slug: m.Slug, Prompts: slices.Clone(m.Prompts), Loaded: true}
	case WorkspaceEntered:
		s.Busy, s.Gates = "", Gates{}
		s.Workspace, s.Seq, s.NeedSnapshot = m.Workspace, 0, true
		s.Channels, s.Members, s.Messages, s.Online, s.Pending = nil, nil, nil, nil, nil
	case SendRequested:
		p := Pending{ClientCmdID: m.ClientCmdID, ChannelID: m.ChannelID, Body: m.Body,
			At: m.At, Order: s.nextOrder, Status: Sending}
		s.nextOrder++
		if err := (protocol.SendMessage{ChannelID: m.ChannelID, Body: m.Body}).Validate(); err != nil {
			p.Status, p.Err = Failed, &protocol.Error{Code: "invalid", Message: err.Error()}
		}
		s.Pending = with(s.Pending, p.ClientCmdID, p)
	}
	return s
}

func reduceConn(s State, m ConnChanged) State {
	next, err := Transition(s.Conn, m.To)
	if err != nil {
		s.IllegalTransitions++
		return s
	}
	from := s.Conn
	s.Conn, s.ConnDetail = next, m.Detail
	if from == Connecting && next == Resyncing {
		// Presence is never replayed: start from "only me" and let the
		// snapshot or the server's first presence frame fill in the rest.
		s.Online = nil
		if s.Auth.UserID != "" {
			s.Online = map[string]bool{s.Auth.UserID: true}
		}
	}
	return s
}

func reduceFrame(s State, e protocol.Envelope) State {
	if e.WorkspaceID != "" && s.Workspace.ID != "" && e.WorkspaceID != s.Workspace.ID {
		return drop(s, "frame for another workspace")
	}
	switch e.Kind {
	case protocol.KindSnapshot:
		snap, err := protocol.DecodePayload[protocol.Snapshot](e)
		if err != nil {
			return drop(s, err.Error())
		}
		s = applySnapshot(s, snap)
		if s.Conn == Resyncing {
			s.Conn, s.ConnDetail = Live, ""
		}
	case protocol.KindEvent:
		switch {
		case s.NeedSnapshot:
			return drop(s, "event before snapshot")
		case e.Seq <= s.Seq:
			return drop(s, "")
		case e.Seq != s.Seq+1:
			// The server promises gap-free seqs, so a gap means we missed
			// something: start over from a snapshot.
			s.NeedSnapshot = true
			s.ProtocolErr = "gap in seq"
			if s.Conn == Live {
				s.Conn = Resyncing
			}
			return s
		}
		s = applyEvent(s, e)
		s.Seq = e.Seq
	case protocol.KindAck:
		p, ok := s.Pending[e.ClientCmdID]
		if !ok {
			return s // not ours, or already confirmed by its event
		}
		if e.Error != nil {
			p.Status, p.Err = Failed, e.Error
		} else if p.Status == Sending {
			p.Status, p.AckSeq = Acked, e.Seq
		}
		s.Pending = with(s.Pending, p.ClientCmdID, p)
	case protocol.KindPresence:
		pr, err := protocol.DecodePayload[protocol.Presence](e)
		if err != nil {
			return drop(s, err.Error())
		}
		online := maps.Clone(s.Online)
		if online == nil {
			online = map[string]bool{}
		}
		for _, c := range pr.Changes {
			if c.Status == protocol.Online {
				online[c.UserID] = true
			} else {
				delete(online, c.UserID)
			}
		}
		s.Online = online
		// By convention the server sends presence once the replay is done.
		if s.Conn == Resyncing && !s.NeedSnapshot {
			s.Conn, s.ConnDetail = Live, ""
		}
	case protocol.KindResync:
		s.Seq, s.NeedSnapshot = 0, true
		s.Channels, s.Members, s.Messages, s.Online = nil, nil, nil, nil
		if s.Conn == Live {
			s.Conn = Resyncing
		}
	}
	return s
}

func drop(s State, why string) State {
	s.Dropped++
	if why != "" {
		s.ProtocolErr = why
	}
	return s
}

func applyEvent(s State, e protocol.Envelope) State {
	switch e.Type {
	case protocol.TypeMemberJoined:
		ev, err := protocol.DecodePayload[protocol.MemberJoined](e)
		if err != nil {
			s.ProtocolErr = err.Error()
			return s
		}
		s.Members = upsert(s.Members, ev.Member, func(m protocol.Member) bool { return m.UserID == ev.Member.UserID })
	case protocol.TypeChannelCreated:
		ev, err := protocol.DecodePayload[protocol.ChannelCreated](e)
		if err != nil {
			s.ProtocolErr = err.Error()
			return s
		}
		s.Channels = upsert(s.Channels, ev.Channel, func(c protocol.Channel) bool { return c.ID == ev.Channel.ID })
	case protocol.TypeMessagePosted:
		ev, err := protocol.DecodePayload[protocol.MessagePosted](e)
		if err != nil {
			s.ProtocolErr = err.Error()
			return s
		}
		msg := ev.Message
		list := s.Messages[msg.ChannelID]
		if !slices.ContainsFunc(list, func(m protocol.Message) bool { return m.ID == msg.ID }) {
			list = append(slices.Clip(list), msg)
			s.Messages = with(s.Messages, msg.ChannelID, list)
		}
		if msg.ClientCmdID != "" && msg.AuthorID == s.Auth.UserID {
			s.Pending = without(s.Pending, msg.ClientCmdID)
		}
	}
	return s
}

func applySnapshot(s State, snap protocol.Snapshot) State {
	s.Seq, s.NeedSnapshot = snap.Seq, false
	s.Workspace = snap.Workspace
	s.Members = slices.Clone(snap.Members)
	s.Channels = slices.Clone(snap.Channels)
	s.Messages = map[string][]protocol.Message{}
	confirmed := map[string]bool{}
	for _, m := range snap.Messages {
		s.Messages[m.ChannelID] = append(s.Messages[m.ChannelID], m)
		if m.ClientCmdID != "" && m.AuthorID == s.Auth.UserID {
			confirmed[m.ClientCmdID] = true
		}
	}
	s.Online = map[string]bool{}
	for _, id := range snap.Online {
		s.Online[id] = true
	}
	if len(s.Pending) > 0 {
		pending := map[string]Pending{}
		for id, p := range s.Pending {
			// Gone: in the snapshot, or acked at or before it (so it is in
			// the server's history, maybe older than the snapshot window).
			if confirmed[id] || p.Status == Acked && p.AckSeq <= snap.Seq {
				continue
			}
			pending[id] = p
		}
		s.Pending = pending
	}
	return s
}

// with returns a copy of m with k set to v.
func with[K comparable, V any](m map[K]V, k K, v V) map[K]V {
	out := make(map[K]V, len(m)+1)
	maps.Copy(out, m)
	out[k] = v
	return out
}

// without returns a copy of m without k (m itself if k is absent).
func without[K comparable, V any](m map[K]V, k K) map[K]V {
	if _, ok := m[k]; !ok {
		return m
	}
	out := maps.Clone(m)
	delete(out, k)
	return out
}

// upsert returns a copy of list with v replacing the first match, or appended.
func upsert[T any](list []T, v T, match func(T) bool) []T {
	out := slices.Clone(list)
	if i := slices.IndexFunc(out, match); i >= 0 {
		out[i] = v
		return out
	}
	return append(out, v)
}
