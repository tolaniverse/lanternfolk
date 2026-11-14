package protocol

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	in := Envelope{
		Kind:        KindCommand,
		WorkspaceID: "ws",
		ClientCmdID: "c1",
		Type:        "SendMessage",
		Payload:     json.RawMessage(`{"body":"hi"}`),
	}
	b, err := Encode(in)
	if err != nil {
		t.Fatal(err)
	}
	out, err := Decode(b)
	if err != nil {
		t.Fatal(err)
	}
	if out.V != Version || out.Type != in.Type || string(out.Payload) != string(in.Payload) {
		t.Errorf("round trip = %+v", out)
	}
}

func TestDecodeRejects(t *testing.T) {
	cases := map[string]struct {
		frame string
		want  error
	}{
		"not json":          {`{`, ErrInvalidEnvelope},
		"future version":    {`{"v":99,"kind":"presence"}`, ErrUnsupportedVersion},
		"missing version":   {`{"kind":"presence"}`, ErrUnsupportedVersion},
		"unknown kind":      {`{"v":1,"kind":"teleport"}`, ErrInvalidEnvelope},
		"cmd without id":    {`{"v":1,"kind":"cmd","type":"SendMessage"}`, ErrInvalidEnvelope},
		"event without seq": {`{"v":1,"kind":"event","type":"MessagePosted"}`, ErrInvalidEnvelope},
	}
	for name, c := range cases {
		if _, err := Decode([]byte(c.frame)); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", name, err, c.want)
		}
	}
}

func TestSupported(t *testing.T) {
	if !Supported(Version) {
		t.Error("current version must be supported")
	}
	if Supported(0) || Supported(Version+1) {
		t.Error("0 and future versions must be rejected")
	}
}
