package integration

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/tolaniverse/lanternfolk/internal/adapters/sqlite"
	"github.com/tolaniverse/lanternfolk/internal/client/transport"
	"github.com/tolaniverse/lanternfolk/internal/server/app"
	"github.com/tolaniverse/lanternfolk/internal/server/gateway"
	"github.com/tolaniverse/lanternfolk/internal/server/realtime"
	"github.com/tolaniverse/lanternfolk/protocol"
)

func TestRealClientAgainstServer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "db.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	wake := make(chan struct{}, 1)
	hub := realtime.New()
	a := app.New(store, func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	})
	go func() { _ = realtime.Relay(ctx, store, hub.Publish, wake) }()
	srv := httptest.NewServer(gateway.New(slog.New(slog.NewTextHandler(io.Discard, nil))).WithWorkspaceLoop(a, hub, true))
	defer srv.Close()
	client := transport.NewHTTP(srv.URL)
	user, err := client.AuthDev(ctx, "ada")
	if err != nil {
		t.Fatal(err)
	}
	w, err := client.CreateWorkspace(ctx, user.Token, protocol.CreateWorkspace{Name: "Lamp Guild", Slug: "lamp-guild"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := client.Dial(ctx, user.Token, w.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	first, err := stream.Recv(ctx)
	if err != nil || first.Kind != protocol.KindSnapshot {
		t.Fatalf("first: %+v %v", first, err)
	}
	snap, err := protocol.DecodePayload[protocol.Snapshot](first)
	if err != nil || len(snap.Channels) != 1 {
		t.Fatalf("snapshot: %+v %v", snap, err)
	}
	command, err := protocol.NewCommand(protocol.TypeSendMessage, "client-1", w.ID, protocol.SendMessage{ChannelID: snap.Channels[0].ID, Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if err := stream.Send(ctx, command); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		event, err := stream.Recv(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if event.Kind == protocol.KindEvent && event.Type == protocol.TypeMessagePosted {
			return
		}
	}
	t.Fatal("real transport did not receive the posted message")
}
