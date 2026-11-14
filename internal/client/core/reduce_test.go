package core

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"reflect"
	"testing"
	"time"

	"github.com/tolaniverse/lanternfolk/protocol"
)

const (
	wsID = "ws1"
	me   = "u-me"
	you  = "u-you"
	gen  = "c-general"
)

var t0 = time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)

func payload(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func event(seq uint64, typ string, v any) FrameReceived {
	return FrameReceived{protocol.Envelope{V: protocol.Version, Kind: protocol.KindEvent,
		WorkspaceID: wsID, Seq: seq, Type: typ, Payload: payload(v)}}
}

func posted(seq uint64, id, channel, author, body, cmd string) FrameReceived {
	return event(seq, protocol.TypeMessagePosted, protocol.MessagePosted{Message: protocol.Message{
		ID: id, ChannelID: channel, AuthorID: author, Body: body, CreatedAt: t0, ClientCmdID: cmd}})
}

func snapshot(s protocol.Snapshot) FrameReceived {
	return FrameReceived{protocol.Envelope{V: protocol.Version, Kind: protocol.KindSnapshot,
		WorkspaceID: wsID, Payload: payload(s)}}
}

func ack(cmd string, seq uint64, err *protocol.Error) FrameReceived {
	return FrameReceived{protocol.Envelope{V: protocol.Version, Kind: protocol.KindAck,
		WorkspaceID: wsID, ClientCmdID: cmd, Seq: seq, Error: err}}
}

func presence(changes ...protocol.PresenceChange) FrameReceived {
	return FrameReceived{protocol.Envelope{V: protocol.Version, Kind: protocol.KindPresence,
		WorkspaceID: wsID, Payload: payload(protocol.Presence{Changes: changes})}}
}

func resync() FrameReceived {
	return FrameReceived{protocol.Envelope{V: protocol.Version, Kind: protocol.KindResync, WorkspaceID: wsID}}
}

func baseSnapshot() protocol.Snapshot {
	return protocol.Snapshot{
		Seq:       2,
		Workspace: protocol.Workspace{ID: wsID, Name: "Hall", Slug: "hall"},
		Members:   []protocol.Member{{UserID: me, Handle: "me", Role: protocol.RoleOwner}},
		Channels:  []protocol.Channel{{ID: gen, Name: "general"}},
		Online:    []string{me},
	}
}

// live is a signed-in client, Live in workspace ws1 at seq 2.
func live() State {
	s := State{}
	for _, m := range []Msg{
		SignedIn{protocol.AuthResponse{Token: "t", UserID: me, Handle: "me"}},
		WorkspaceEntered{protocol.Workspace{ID: wsID, Name: "Hall", Slug: "hall"}},
		ConnChanged{To: Connecting}, ConnChanged{To: Resyncing},
		snapshot(baseSnapshot()),
	} {
		s = Reduce(s, m)
	}
	return s
}

func reduceAll(s State, msgs ...Msg) State {
	for _, m := range msgs {
		s = Reduce(s, m)
	}
	return s
}

func TestReduce(t *testing.T) {
	tests := []struct {
		name  string
		start State
		msgs  []Msg
		check func(t *testing.T, s State)
	}{
		{"snapshot makes the client live", State{}, []Msg{
			SignedIn{protocol.AuthResponse{Token: "t", UserID: me}},
			WorkspaceEntered{protocol.Workspace{ID: wsID}},
			ConnChanged{To: Connecting}, ConnChanged{To: Resyncing}, snapshot(baseSnapshot()),
		}, func(t *testing.T, s State) {
			if s.Conn != Live || s.Seq != 2 || s.NeedSnapshot || s.Workspace.Name != "Hall" || !s.Online[me] {
				t.Fatalf("%+v", s)
			}
		}},
		{"events apply in order", live(), []Msg{
			event(3, protocol.TypeMemberJoined, protocol.MemberJoined{Member: protocol.Member{UserID: you, Handle: "you"}}),
			event(4, protocol.TypeChannelCreated, protocol.ChannelCreated{Channel: protocol.Channel{ID: "c2", Name: "random"}}),
			posted(5, "m1", gen, you, "hi", ""),
		}, func(t *testing.T, s State) {
			if s.Seq != 5 || len(s.Members) != 2 || len(s.Channels) != 2 || len(s.Messages[gen]) != 1 {
				t.Fatalf("%+v", s)
			}
			if s.Handle(you) != "you" || s.Handle(me) != "me" || s.Handle("u-unknown-long") != "u-unknow" {
				t.Fatal("Handle")
			}
		}},
		{"duplicate and old seqs are dropped", live(), []Msg{
			posted(3, "m1", gen, you, "hi", ""),
			posted(3, "m1", gen, you, "hi", ""),
			posted(2, "m0", gen, you, "old", ""),
			posted(1, "m-1", gen, you, "older", ""),
		}, func(t *testing.T, s State) {
			if s.Seq != 3 || len(s.Messages[gen]) != 1 || s.Dropped != 3 {
				t.Fatalf("seq %d msgs %d dropped %d", s.Seq, len(s.Messages[gen]), s.Dropped)
			}
		}},
		{"a gap asks for a snapshot", live(), []Msg{posted(5, "m1", gen, you, "hi", "")},
			func(t *testing.T, s State) {
				if !s.NeedSnapshot || s.Seq != 2 || s.Conn != Resyncing || s.Since() != 0 || len(s.Messages[gen]) != 0 {
					t.Fatalf("%+v", s)
				}
			}},
		{"events before the first snapshot are dropped", State{Workspace: protocol.Workspace{ID: wsID}, NeedSnapshot: true},
			[]Msg{posted(1, "m1", gen, you, "hi", "")}, func(t *testing.T, s State) {
				if s.Seq != 0 || s.Dropped != 1 {
					t.Fatalf("%+v", s)
				}
			}},
		{"frames for another workspace are dropped", live(), []Msg{FrameReceived{protocol.Envelope{
			V: 1, Kind: protocol.KindEvent, WorkspaceID: "other", Seq: 3, Type: protocol.TypeMessagePosted}}},
			func(t *testing.T, s State) {
				if s.Seq != 2 || s.Dropped != 1 {
					t.Fatalf("%+v", s)
				}
			}},
		{"unknown event types still advance seq", live(), []Msg{event(3, "Something", map[string]int{"x": 1})},
			func(t *testing.T, s State) {
				if s.Seq != 3 {
					t.Fatalf("%+v", s)
				}
			}},
		{"presence changes the online set", live(), []Msg{
			presence(protocol.PresenceChange{UserID: you, Status: protocol.Online}),
			presence(protocol.PresenceChange{UserID: me, Status: protocol.Offline}),
		}, func(t *testing.T, s State) {
			if !s.Online[you] || s.Online[me] || len(s.Online) != 1 {
				t.Fatalf("%v", s.Online)
			}
		}},
		{"resync clears to needs snapshot", reduceAll(live(), posted(3, "m1", gen, you, "hi", "")),
			[]Msg{resync()}, func(t *testing.T, s State) {
				if !s.NeedSnapshot || s.Seq != 0 || s.Conn != Resyncing || s.Messages != nil || s.Channels != nil {
					t.Fatalf("%+v", s)
				}
				if s.Workspace.ID != wsID || !s.SignedIn() {
					t.Fatal("resync forgot the workspace or the token")
				}
			}},
		{"reconnect replays then presence makes it live", live(), []Msg{
			ConnChanged{To: Backoff}, ConnChanged{To: Connecting}, ConnChanged{To: Resyncing},
			posted(3, "m1", gen, you, "missed", ""),
			presence(protocol.PresenceChange{UserID: you, Status: protocol.Online}),
		}, func(t *testing.T, s State) {
			if s.Conn != Live || s.Seq != 3 || !s.Online[you] || !s.Online[me] {
				t.Fatalf("%+v", s)
			}
		}},
		{"caught up without presence", live(), []Msg{
			ConnChanged{To: Backoff}, ConnChanged{To: Connecting}, ConnChanged{To: Resyncing}, CaughtUp{},
		}, func(t *testing.T, s State) {
			if s.Conn != Live {
				t.Fatalf("%s", s.Conn)
			}
		}},
		{"caught up is ignored while a snapshot is needed", reduceAll(live(), resync()), []Msg{CaughtUp{}},
			func(t *testing.T, s State) {
				if s.Conn != Resyncing {
					t.Fatalf("%s", s.Conn)
				}
			}},
		{"operations set busy and notices", State{}, []Msg{
			OpStarted{OpJoin}, OpFailed{OpJoin, "wrong answer"},
		}, func(t *testing.T, s State) {
			if s.Busy != "" || s.Notice != (Notice{OpJoin, "wrong answer"}) {
				t.Fatalf("%+v", s)
			}
			s = Reduce(s, OpStarted{OpGates})
			if s.Busy != OpGates || s.Notice != (Notice{}) {
				t.Fatalf("%+v", s)
			}
			s = Reduce(s, GatesLoaded{"hall", []protocol.GatePrompt{{Kind: protocol.GateRiddle, Prompt: "?"}}})
			if s.Busy != "" || !s.Gates.Loaded || s.Gates.Slug != "hall" || len(s.Gates.Prompts) != 1 {
				t.Fatalf("%+v", s)
			}
		}},
		{"undecodable snapshot is dropped", live(), []Msg{FrameReceived{protocol.Envelope{
			V: 1, Kind: protocol.KindSnapshot, Payload: json.RawMessage(`"nope"`)}}},
			func(t *testing.T, s State) {
				if s.Dropped != 1 || s.ProtocolErr == "" || s.Seq != 2 {
					t.Fatalf("%+v", s)
				}
			}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tc.check(t, reduceAll(tc.start, tc.msgs...))
		})
	}
}

func TestOptimisticSend(t *testing.T) {
	send := SendRequested{ClientCmdID: "c1", ChannelID: gen, Body: "hello", At: t0}
	tests := []struct {
		name        string
		msgs        []Msg
		wantPending map[string]PendingStatus
		wantStatus  []EntryStatus // the timeline of #general
	}{
		{"pending until confirmed", []Msg{send}, map[string]PendingStatus{"c1": Sending}, []EntryStatus{InFlight}},
		{"ack then event confirms", []Msg{send, ack("c1", 3, nil), posted(3, "m1", gen, me, "hello", "c1")},
			map[string]PendingStatus{}, []EntryStatus{Confirmed}},
		{"event before ack confirms, late ack is ignored", []Msg{send, posted(3, "m1", gen, me, "hello", "c1"), ack("c1", 3, nil)},
			map[string]PendingStatus{}, []EntryStatus{Confirmed}},
		{"ack alone keeps it pending as acked", []Msg{send, ack("c1", 3, nil)},
			map[string]PendingStatus{"c1": Acked}, []EntryStatus{InFlight}},
		{"error ack fails it", []Msg{send, ack("c1", 0, &protocol.Error{Code: "forbidden", Message: "no"})},
			map[string]PendingStatus{"c1": Failed}, []EntryStatus{Rejected}},
		{"someone else's cmd id does not confirm ours", []Msg{send, posted(3, "m1", gen, you, "hey", "c1")},
			map[string]PendingStatus{"c1": Sending}, []EntryStatus{Confirmed, InFlight}},
		{"an invalid body fails at once", []Msg{SendRequested{ClientCmdID: "c2", ChannelID: gen, Body: "   "}},
			map[string]PendingStatus{"c2": Failed}, []EntryStatus{Rejected}},
		{"snapshot containing it confirms", []Msg{send, snapshotWith(3, protocol.Message{ID: "m1", ChannelID: gen, AuthorID: me, Body: "hello", ClientCmdID: "c1"})},
			map[string]PendingStatus{}, []EntryStatus{Confirmed}},
		{"snapshot after its ack seq drops it", []Msg{send, ack("c1", 3, nil), snapshotWith(9)},
			map[string]PendingStatus{}, []EntryStatus{}},
		{"snapshot keeps unsent ones", []Msg{send, snapshotWith(9)},
			map[string]PendingStatus{"c1": Sending}, []EntryStatus{InFlight}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := reduceAll(live(), tc.msgs...)
			got := map[string]PendingStatus{}
			for id, p := range s.Pending {
				got[id] = p.Status
			}
			if !reflect.DeepEqual(got, tc.wantPending) {
				t.Errorf("pending = %v, want %v", got, tc.wantPending)
			}
			var statuses []EntryStatus
			for _, e := range s.Timeline(gen) {
				statuses = append(statuses, e.Status)
				if e.Status == Rejected && e.Err == "" {
					t.Error("rejected entry has no error text")
				}
			}
			if len(statuses) != len(tc.wantStatus) || (len(statuses) > 0 && !reflect.DeepEqual(statuses, tc.wantStatus)) {
				t.Errorf("timeline = %v, want %v", statuses, tc.wantStatus)
			}
		})
	}
}

func snapshotWith(seq uint64, msgs ...protocol.Message) FrameReceived {
	s := baseSnapshot()
	s.Seq, s.Messages = seq, msgs
	return snapshot(s)
}

func TestReduceDoesNotMutateItsInput(t *testing.T) {
	s := reduceAll(live(), SendRequested{ClientCmdID: "c1", ChannelID: gen, Body: "a", At: t0},
		posted(3, "m1", gen, you, "hi", ""))
	before := fmt.Sprintf("%+v", s)
	msgs := []Msg{
		posted(4, "m2", gen, you, "more", ""), ack("c1", 5, nil), posted(5, "m3", gen, me, "a", "c1"),
		presence(protocol.PresenceChange{UserID: you, Status: protocol.Online}),
		event(6, protocol.TypeMemberJoined, protocol.MemberJoined{Member: protocol.Member{UserID: you}}),
		event(7, protocol.TypeChannelCreated, protocol.ChannelCreated{Channel: protocol.Channel{ID: "c9"}}),
		SendRequested{ClientCmdID: "c2", ChannelID: gen, Body: "b"}, resync(), snapshotWith(9),
	}
	for _, m := range msgs {
		_ = Reduce(s, m)
		if after := fmt.Sprintf("%+v", s); after != before {
			t.Fatalf("Reduce(%T) mutated its input:\nbefore %s\nafter  %s", m, before, after)
		}
	}
}

func TestRoster(t *testing.T) {
	s := reduceAll(live(),
		event(3, protocol.TypeMemberJoined, protocol.MemberJoined{Member: protocol.Member{UserID: "u-b", Handle: "bea"}}),
		event(4, protocol.TypeMemberJoined, protocol.MemberJoined{Member: protocol.Member{UserID: "u-a", Handle: "abe"}}),
		presence(protocol.PresenceChange{UserID: "u-b", Status: protocol.Online}))
	var got []string
	for _, r := range s.Roster() {
		got = append(got, fmt.Sprintf("%s:%v", r.Handle, r.Online))
	}
	want := []string{"bea:true", "me:true", "abe:false"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("roster = %v, want %v", got, want)
	}
	if s.DefaultChannel() != gen || (State{}).DefaultChannel() != "" {
		t.Fatal("DefaultChannel")
	}
}

// TestEventLogMatchesSnapshot is the property from LLD §6: reducing an event
// log in order gives the same state as loading the snapshot taken at the end
// of that log. Logs are random; duplicates are mixed in to show they change
// nothing.
func TestEventLogMatchesSnapshot(t *testing.T) {
	for seed := range uint64(300) {
		rng := rand.New(rand.NewPCG(seed, 42))
		log, snap := randomHistory(rng)

		viaLog := State{Auth: protocol.AuthResponse{Token: "t", UserID: me}}
		viaLog = reduceAll(viaLog, WorkspaceEntered{snap.Workspace}, ConnChanged{To: Connecting}, ConnChanged{To: Resyncing})
		// Start from the empty snapshot at seq 0, then replay with duplicates.
		viaLog = Reduce(viaLog, snapshot(protocol.Snapshot{Workspace: snap.Workspace}))
		for i, e := range log {
			viaLog = Reduce(viaLog, e)
			if rng.IntN(4) == 0 {
				viaLog = Reduce(viaLog, log[rng.IntN(i+1)]) // redelivery
			}
		}
		viaSnap := State{Auth: protocol.AuthResponse{Token: "t", UserID: me}}
		viaSnap = reduceAll(viaSnap, WorkspaceEntered{snap.Workspace}, ConnChanged{To: Connecting},
			ConnChanged{To: Resyncing}, snapshot(snap))

		if got, want := durableOf(viaLog), durableOf(viaSnap); !reflect.DeepEqual(got, want) {
			t.Fatalf("seed %d: log and snapshot disagree\nlog:  %+v\nsnap: %+v", seed, got, want)
		}
	}
}

type durable struct {
	Seq       uint64
	Workspace protocol.Workspace
	Members   []protocol.Member
	Channels  []protocol.Channel
	Messages  map[string][]protocol.Message
}

func durableOf(s State) durable {
	msgs := map[string][]protocol.Message{}
	for k, v := range s.Messages {
		if len(v) > 0 {
			msgs[k] = v
		}
	}
	return durable{s.Seq, s.Workspace, s.Members, s.Channels, msgs}
}

// randomHistory builds a random event log and, independently, the snapshot
// a server would serve at its end.
func randomHistory(rng *rand.Rand) ([]Msg, protocol.Snapshot) {
	snap := protocol.Snapshot{Workspace: protocol.Workspace{ID: wsID, Name: "Hall", Slug: "hall"}}
	var log []Msg
	seq := uint64(0)
	next := func() uint64 { seq++; return seq }
	addChannel := func() {
		c := protocol.Channel{ID: fmt.Sprintf("c%d", len(snap.Channels)), Name: fmt.Sprintf("ch%d", len(snap.Channels))}
		snap.Channels = append(snap.Channels, c)
		log = append(log, event(next(), protocol.TypeChannelCreated, protocol.ChannelCreated{Channel: c}))
	}
	addMember := func() {
		m := protocol.Member{UserID: fmt.Sprintf("u%d", len(snap.Members)), Handle: fmt.Sprintf("h%d", len(snap.Members)), Role: protocol.RoleMember}
		if len(snap.Members) == 0 {
			m.Role = protocol.RoleOwner
		}
		snap.Members = append(snap.Members, m)
		log = append(log, event(next(), protocol.TypeMemberJoined, protocol.MemberJoined{Member: m}))
	}
	addChannel()
	addMember()
	for range rng.IntN(60) {
		switch r := rng.IntN(10); {
		case r == 0:
			addChannel()
		case r == 1:
			addMember()
		default:
			c := snap.Channels[rng.IntN(len(snap.Channels))]
			author := snap.Members[rng.IntN(len(snap.Members))]
			m := protocol.Message{ID: fmt.Sprintf("m%03d", seq+1), ChannelID: c.ID, AuthorID: author.UserID,
				Body: fmt.Sprintf("body %d", rng.Int()), CreatedAt: t0.Add(time.Duration(seq) * time.Second)}
			snap.Messages = append(snap.Messages, m)
			log = append(log, event(next(), protocol.TypeMessagePosted, protocol.MessagePosted{Message: m}))
		}
	}
	snap.Seq = seq
	return log, snap
}
