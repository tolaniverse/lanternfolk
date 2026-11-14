// Package core is the client's pure state: State, the messages that change
// it, and Reduce (LLD §4.6). It does no I/O and keeps no clock, so the same
// log of messages always gives the same State.
//
// A State value is immutable once Reduce returns it: Reduce copies any map
// or slice it changes, so snapshots handed to UIs never change under them.
// Callers must not mutate the maps and slices in a State either.
package core

import (
	"time"

	"github.com/tolaniverse/lanternfolk/protocol"
)

// Op names an HTTP operation the session runs for the user.
type Op string

const (
	OpSignIn Op = "sign in"
	OpCreate Op = "create workspace"
	OpGates  Op = "fetch gates"
	OpJoin   Op = "join workspace"
)

// Notice is the outcome of the last failed operation, shown to the user.
type Notice struct {
	Op   Op
	Text string
}

// Gates are the prompts of the workspace the user is trying to join.
type Gates struct {
	Slug    string
	Prompts []protocol.GatePrompt
	Loaded  bool
}

// PendingStatus is where an optimistic command is.
type PendingStatus uint8

const (
	Sending PendingStatus = iota // written (or waiting to be written) to the socket
	Acked                        // the server accepted it; its event is on the way
	Failed                       // the server, or local validation, rejected it
)

func (p PendingStatus) String() string {
	switch p {
	case Sending:
		return "sending"
	case Acked:
		return "acked"
	case Failed:
		return "failed"
	}
	return "unknown"
}

// Pending is an optimistic SendMessage, keyed by its client_cmd_id until
// the MessagePosted event carrying the same id replaces it.
type Pending struct {
	ClientCmdID string
	ChannelID   string
	Body        string
	At          time.Time
	Order       uint64 // local creation order, for resending and display
	Status      PendingStatus
	AckSeq      uint64
	Err         *protocol.Error
}

// State is everything the client knows.
type State struct {
	Conn       ConnState
	ConnDetail string // e.g. "retrying in 2s: connection refused"

	Auth   protocol.AuthResponse // zero until signed in
	Busy   Op                    // the operation in flight, if any
	Notice Notice                // the last failure, cleared when the next operation starts
	Gates  Gates

	Workspace    protocol.Workspace // zero until created or joined
	Seq          uint64             // last applied event
	NeedSnapshot bool               // the next connect must ask for since=0
	Channels     []protocol.Channel
	Members      []protocol.Member
	Messages     map[string][]protocol.Message // by channel ID, oldest first
	Online       map[string]bool               // user IDs
	Pending      map[string]Pending            // by client_cmd_id

	// Diagnostics, for tests and a debug line.
	Dropped            int    // frames ignored: duplicate seq, foreign workspace, undecodable
	IllegalTransitions int    // ConnChanged messages the state machine rejected
	ProtocolErr        string // the last frame the client could not use

	nextOrder uint64
}

// SignedIn reports whether the user has a token.
func (s State) SignedIn() bool { return s.Auth.Token != "" }

// InWorkspace reports whether the user has created or joined a workspace.
func (s State) InWorkspace() bool { return s.Workspace.ID != "" }

// Since is the cursor to reconnect with: 0 asks the server for a snapshot.
func (s State) Since() uint64 {
	if s.NeedSnapshot {
		return 0
	}
	return s.Seq
}
