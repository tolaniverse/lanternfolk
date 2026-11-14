package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/tolaniverse/lanternfolk/internal/adapters/sqlite"
	"github.com/tolaniverse/lanternfolk/internal/server/app"
	"github.com/tolaniverse/lanternfolk/internal/server/gateway"
	"github.com/tolaniverse/lanternfolk/internal/server/realtime"
	"github.com/tolaniverse/lanternfolk/protocol"
)

func TestWorkspaceLoop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "test.db"))
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
	g := gateway.New(slog.New(slog.NewTextHandler(io.Discard, nil))).WithWorkspaceLoop(a, hub, true)
	srv := httptest.NewServer(g)
	defer srv.Close()

	request := func(method, path, token string, payload any, out any) int {
		t.Helper()
		b, _ := json.Marshal(payload)
		r, err := http.NewRequestWithContext(ctx, method, srv.URL+path, bytes.NewReader(b))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		if out != nil && res.StatusCode < 300 {
			if err := json.NewDecoder(res.Body).Decode(out); err != nil {
				t.Fatal(err)
			}
		}
		return res.StatusCode
	}
	var owner, guest protocol.AuthResponse
	if s := request("POST", "/v1/auth/dev", "", protocol.AuthDevRequest{Handle: "owner"}, &owner); s != 200 {
		t.Fatal(s)
	}
	if s := request("POST", "/v1/auth/dev", "", protocol.AuthDevRequest{Handle: "guest"}, &guest); s != 200 {
		t.Fatal(s)
	}
	var ws protocol.Workspace
	create := protocol.CreateWorkspace{Name: "Lamp Guild", Slug: "lamp-guild", Gates: []protocol.GateSpec{{Kind: protocol.GateRiddle, Prompt: "Password?", Answer: "Mellon"}}}
	if s := request("POST", "/v1/workspaces", owner.Token, create, &ws); s != 201 {
		t.Fatal(s)
	}
	var prompts []protocol.GatePrompt
	if s := request("GET", "/v1/workspaces/lamp-guild/gates", "", nil, &prompts); s != 200 || len(prompts) != 1 {
		t.Fatalf("gates: %d %+v", s, prompts)
	}
	if s := request("POST", "/v1/workspaces/lamp-guild/join", guest.Token, protocol.JoinWorkspace{Answers: []string{"wrong"}}, nil); s != 400 {
		t.Fatalf("wrong gate: %d", s)
	}
	if s := request("POST", "/v1/workspaces/lamp-guild/join", guest.Token, protocol.JoinWorkspace{Answers: []string{" MELLON "}}, nil); s != 200 {
		t.Fatalf("right gate: %d", s)
	}

	connect := func(token string, since uint64) *websocket.Conn {
		t.Helper()
		u := "ws" + strings.TrimPrefix(srv.URL, "http") + fmt.Sprintf("/v1/ws?workspace=%s&since=%d", ws.ID, since)
		c, res, err := websocket.Dial(ctx, u, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}}})
		if err != nil {
			if res != nil {
				t.Fatalf("dial %d: %v", res.StatusCode, err)
			}
			t.Fatal(err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	read := func(c *websocket.Conn) protocol.Envelope {
		t.Helper()
		cctx, done := context.WithTimeout(ctx, 3*time.Second)
		defer done()
		_, b, err := c.Read(cctx)
		if err != nil {
			t.Fatal(err)
		}
		e, err := protocol.Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	first := connect(owner.Token, 0)
	snapshot := read(first)
	if snapshot.Kind != protocol.KindSnapshot {
		t.Fatalf("first = %+v", snapshot)
	}
	s, err := protocol.DecodePayload[protocol.Snapshot](snapshot)
	if err != nil || len(s.Channels) != 1 || len(s.Members) != 2 {
		t.Fatalf("snapshot: %+v %v", s, err)
	}
	guestConn := connect(guest.Token, 0)
	if got := read(guestConn); got.Kind != protocol.KindSnapshot {
		t.Fatalf("guest first = %+v", got)
	}
	cmd, err := protocol.NewCommand(protocol.TypeSendMessage, "cmd-1", ws.ID, protocol.SendMessage{ChannelID: s.Channels[0].ID, Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := protocol.Encode(cmd)
	if err := first.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	var ack, event protocol.Envelope
	for i := 0; i < 8 && (ack.Kind == "" || event.Kind == ""); i++ {
		e := read(first)
		if e.Kind == protocol.KindAck {
			ack = e
		}
		if e.Kind == protocol.KindEvent && e.Type == protocol.TypeMessagePosted {
			event = e
		}
	}
	if ack.Seq == 0 || event.Seq != ack.Seq {
		t.Fatalf("ack %+v event %+v", ack, event)
	}
	// Duplicate command returns the same seq, without another durable event.
	if err := first.Write(ctx, websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		e := read(first)
		if e.Kind == protocol.KindAck {
			if e.Seq != ack.Seq {
				t.Fatalf("duplicate seq = %d", e.Seq)
			}
			break
		}
	}
	resume := connect(guest.Token, s.Seq)
	var found bool
	for i := 0; i < 8; i++ {
		e := read(resume)
		if e.Kind == protocol.KindEvent && e.Type == protocol.TypeMessagePosted {
			found = true
			break
		}
		if e.Kind == protocol.KindSnapshot {
			t.Fatal("unexpected snapshot on resume")
		}
	}
	if !found {
		t.Fatal("resume lost message")
	}
	// The original guest connection also receives the posted event.
	for i := 0; i < 8; i++ {
		e := read(guestConn)
		if e.Kind == protocol.KindEvent && e.Seq == ack.Seq {
			return
		}
	}
	t.Fatal("live guest did not get the message")
}
