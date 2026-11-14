package client

import (
	"context"
	"testing"
	"time"

	"github.com/tolaniverse/lanternfolk/internal/client/core"
	"github.com/tolaniverse/lanternfolk/internal/client/transport"
	"github.com/tolaniverse/lanternfolk/protocol"
)

func wait(t *testing.T, s *Session, pred func(core.State) bool) core.State {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		st := s.State()
		if pred(st) {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("session state did not become ready")
	return core.State{}
}

func TestFakeWorkspaceLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	f := transport.NewFake()
	a := New(f)
	defer a.Close()
	b := New(f)
	defer b.Close()
	if _, err := a.SignIn(ctx, "ada"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SignIn(ctx, "babbage"); err != nil {
		t.Fatal(err)
	}
	w, err := a.Create(ctx, protocol.CreateWorkspace{Name: "Lamp Guild", Slug: "lamp-guild", Gates: []protocol.GateSpec{{Kind: protocol.GateRiddle, Prompt: "Say friend", Answer: "Mellon"}}})
	if err != nil {
		t.Fatal(err)
	}
	g, err := b.Gates(ctx, w.Slug)
	if err != nil || len(g) != 1 {
		t.Fatalf("gates: %v %v", g, err)
	}
	if _, err := b.Join(ctx, w.Slug, []string{"wrong"}); err == nil {
		t.Fatal("wrong gate accepted")
	}
	if _, err := b.Join(ctx, w.Slug, []string{" MELLON "}); err != nil {
		t.Fatal(err)
	}
	sa := wait(t, a, func(st core.State) bool { return st.Conn == core.Live && len(st.Members) == 2 })
	sb := wait(t, b, func(st core.State) bool { return st.Conn == core.Live && len(st.Online) == 2 })
	if len(sa.Channels) != 1 || len(sb.Channels) != 1 {
		t.Fatal("missing general channel")
	}
	if err := a.Send(ctx, "hello from ada"); err != nil {
		t.Fatal(err)
	}
	wait(t, b, func(st core.State) bool { return len(st.Messages[st.DefaultChannel()]) == 1 })
	if err := b.Send(ctx, "hello from babbage"); err != nil {
		t.Fatal(err)
	}
	wait(t, a, func(st core.State) bool { return len(st.Messages[st.DefaultChannel()]) == 2 && len(st.Pending) == 0 })
}
