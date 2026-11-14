package sqlite_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/tolaniverse/lanternfolk/internal/adapters/sqlite"
	"github.com/tolaniverse/lanternfolk/internal/server/app"
	"github.com/tolaniverse/lanternfolk/protocol"
)

func TestPersistenceAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "workspace.db")
	store, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	a := app.New(store, nil)
	user, err := a.AuthDev(ctx, "keeper")
	if err != nil {
		t.Fatal(err)
	}
	w, err := a.CreateWorkspace(ctx, app.User{ID: user.UserID, Handle: user.Handle}, protocol.CreateWorkspace{
		Name: "Lamp Guild", Slug: "lamp-guild", Gates: []protocol.GateSpec{{Kind: protocol.GateRiddle, Prompt: "Say friend", Answer: "Mellon"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := a.Snapshot(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	a = app.New(store, nil)
	actor, err := a.Authenticate(ctx, user.Token)
	if err != nil || actor.ID != user.UserID {
		t.Fatalf("session after reopen: %+v %v", actor, err)
	}
	after, err := a.Snapshot(ctx, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	if before.Seq != after.Seq || len(after.Channels) != 1 || len(after.Members) != 1 {
		t.Fatalf("snapshot after reopen: %+v", after)
	}
	gates, err := a.Gates(ctx, w.Slug)
	if err != nil || len(gates) != 1 || gates[0].Prompt != "Say friend" {
		t.Fatalf("gate after reopen: %+v %v", gates, err)
	}
	if _, err := a.Join(ctx, app.User{ID: user.UserID, Handle: user.Handle}, w.Slug, protocol.JoinWorkspace{}); err != nil {
		t.Fatalf("idempotent join after reopen: %v", err)
	}
}
