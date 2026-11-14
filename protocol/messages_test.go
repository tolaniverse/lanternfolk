package protocol

import (
	"errors"
	"strings"
	"testing"
)

func TestCreateWorkspaceValidate(t *testing.T) {
	ok := CreateWorkspace{Name: "Lantern Guild", Slug: "lantern-guild",
		Gates: []GateSpec{{Kind: GateRiddle, Prompt: "Speak, friend", Answer: "Mellon"}}}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid workspace rejected: %v", err)
	}
	bad := map[string]CreateWorkspace{
		"empty name":   {Name: " ", Slug: "abc"},
		"short slug":   {Name: "x", Slug: "ab"},
		"upper slug":   {Name: "x", Slug: "Abc"},
		"dash edge":    {Name: "x", Slug: "-abc"},
		"unknown gate": {Name: "x", Slug: "abc", Gates: []GateSpec{{Kind: "dragon", Prompt: "p", Answer: "a"}}},
		"no answer":    {Name: "x", Slug: "abc", Gates: []GateSpec{{Kind: GateRiddle, Prompt: "p", Answer: "  "}}},
		"too many gates": {Name: "x", Slug: "abc", Gates: []GateSpec{
			{GateRiddle, "p", "a"}, {GateRiddle, "p", "a"}, {GateRiddle, "p", "a"}, {GateRiddle, "p", "a"}}},
	}
	for name, c := range bad {
		if err := c.Validate(); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("%s: err = %v, want ErrInvalidPayload", name, err)
		}
	}
}

func TestSendMessageValidate(t *testing.T) {
	if err := (SendMessage{ChannelID: "c", Body: "hi"}).Validate(); err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]SendMessage{
		"no channel": {Body: "hi"},
		"blank":      {ChannelID: "c", Body: " \n "},
		"too long":   {ChannelID: "c", Body: strings.Repeat("é", MaxMessageRunes+1)},
	} {
		if err := s.Validate(); !errors.Is(err, ErrInvalidPayload) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// The limit counts runes, not bytes.
	if err := (SendMessage{ChannelID: "c", Body: strings.Repeat("é", MaxMessageRunes)}).Validate(); err != nil {
		t.Errorf("4000 two-byte runes rejected: %v", err)
	}
}

func TestNormalizeAnswer(t *testing.T) {
	if got := NormalizeAnswer("  Open \t  SESAME "); got != "open sesame" {
		t.Errorf("got %q", got)
	}
}

func TestValidHandle(t *testing.T) {
	for h, want := range map[string]bool{"ada": true, "a": false, "Ada": false, "ada_l": true, "a-b": false} {
		if ValidHandle(h) != want {
			t.Errorf("ValidHandle(%q) = %v", h, !want)
		}
	}
}

func TestCommandRoundTrip(t *testing.T) {
	env, err := NewCommand(TypeSendMessage, "c1", "ws1", SendMessage{ChannelID: "ch", Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	b, err := Encode(env)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	p, err := DecodePayload[SendMessage](got)
	if err != nil || p.Body != "hello" || got.ClientCmdID != "c1" {
		t.Fatalf("round trip: %+v %+v %v", got, p, err)
	}
	if _, err := DecodePayload[SendMessage](Envelope{Type: TypeSendMessage, Payload: []byte(`{"body":1}`)}); !errors.Is(err, ErrInvalidPayload) {
		t.Errorf("bad payload err = %v", err)
	}
}
