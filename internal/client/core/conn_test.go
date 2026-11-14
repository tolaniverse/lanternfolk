package core

import (
	"errors"
	"testing"
)

func TestTransitionTable(t *testing.T) {
	all := []ConnState{Disconnected, Connecting, Resyncing, Live, Backoff}
	legal := map[[2]ConnState]bool{
		{Disconnected, Connecting}: true,
		{Connecting, Resyncing}:    true,
		{Connecting, Backoff}:      true,
		{Connecting, Disconnected}: true,
		{Resyncing, Live}:          true,
		{Resyncing, Backoff}:       true,
		{Resyncing, Disconnected}:  true,
		{Live, Backoff}:            true,
		{Live, Resyncing}:          true,
		{Live, Disconnected}:       true,
		{Backoff, Connecting}:      true,
		{Backoff, Disconnected}:    true,
	}
	for _, from := range all {
		for _, to := range all {
			want := legal[[2]ConnState{from, to}]
			if got := CanTransition(from, to); got != want {
				t.Errorf("CanTransition(%s, %s) = %v, want %v", from, to, got, want)
			}
			next, err := Transition(from, to)
			if want && (err != nil || next != to) {
				t.Errorf("Transition(%s, %s) = %s, %v", from, to, next, err)
			}
			var illegal ErrIllegalTransition
			if !want && (next != from || !errors.As(err, &illegal)) {
				t.Errorf("Transition(%s, %s) = %s, %v; want rejection", from, to, next, err)
			}
		}
	}
}

func TestReduceRejectsIllegalTransitions(t *testing.T) {
	s := Reduce(State{}, ConnChanged{To: Live})
	if s.Conn != Disconnected || s.IllegalTransitions != 1 {
		t.Fatalf("Disconnected → Live accepted: %+v", s)
	}
	path := []ConnState{Connecting, Resyncing, Live, Backoff, Connecting, Resyncing, Live, Disconnected}
	for _, to := range path {
		s = Reduce(s, ConnChanged{To: to})
		if s.Conn != to {
			t.Fatalf("→ %s rejected, at %s", to, s.Conn)
		}
	}
	if s.IllegalTransitions != 1 {
		t.Fatalf("IllegalTransitions = %d", s.IllegalTransitions)
	}
}

func TestStateStrings(t *testing.T) {
	for c, want := range map[ConnState]string{Disconnected: "disconnected", Connecting: "connecting",
		Resyncing: "resyncing", Live: "live", Backoff: "backoff", 9: "ConnState(9)"} {
		if c.String() != want {
			t.Errorf("%d: %q", c, c.String())
		}
	}
}
