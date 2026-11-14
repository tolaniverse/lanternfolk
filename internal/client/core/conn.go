package core

import "fmt"

// ConnState is where the client is in the sync state machine (LLD §4.6):
//
//	Disconnected → Connecting → Resyncing → Live
//	Live → Backoff → Connecting   (the socket dropped)
//
// Resyncing means the socket is open and the server is sending a snapshot
// or replaying missed events; the client goes Live once it has caught up.
type ConnState uint8

const (
	Disconnected ConnState = iota
	Connecting
	Resyncing
	Live
	Backoff
)

func (c ConnState) String() string {
	switch c {
	case Disconnected:
		return "disconnected"
	case Connecting:
		return "connecting"
	case Resyncing:
		return "resyncing"
	case Live:
		return "live"
	case Backoff:
		return "backoff"
	}
	return fmt.Sprintf("ConnState(%d)", uint8(c))
}

// transitions is the whole state machine. Anything not listed is illegal.
//
//   - Connecting → Backoff: the dial failed.
//   - Resyncing → Backoff: the socket dropped before the client caught up.
//   - Live → Resyncing: the server sent resync_required, or the client saw
//     a gap in seq; the client reconnects and loads a fresh snapshot.
//   - any → Disconnected: the session closed, or the server refused for good
//     (bad token, not a member).
var transitions = map[ConnState][]ConnState{
	Disconnected: {Connecting},
	Connecting:   {Resyncing, Backoff, Disconnected},
	Resyncing:    {Live, Backoff, Disconnected},
	Live:         {Backoff, Resyncing, Disconnected},
	Backoff:      {Connecting, Disconnected},
}

// CanTransition reports whether the state machine allows from → to.
// Staying in the same state is not a transition.
func CanTransition(from, to ConnState) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// ErrIllegalTransition is returned by Transition.
type ErrIllegalTransition struct{ From, To ConnState }

func (e ErrIllegalTransition) Error() string {
	return fmt.Sprintf("client: illegal connection transition %s → %s", e.From, e.To)
}

// Transition returns to if from → to is legal, otherwise from and an error.
func Transition(from, to ConnState) (ConnState, error) {
	if !CanTransition(from, to) {
		return from, ErrIllegalTransition{from, to}
	}
	return to, nil
}
