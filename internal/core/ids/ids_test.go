package ids

import (
	"encoding/json"
	"testing"
	"time"
)

func TestNewV7Layout(t *testing.T) {
	u := NewV7()
	if v := u[6] >> 4; v != 7 {
		t.Errorf("version = %d, want 7", v)
	}
	if variant := u[8] >> 6; variant != 0b10 {
		t.Errorf("variant bits = %02b, want 10", variant)
	}
}

func TestTimeRoundTrip(t *testing.T) {
	now := time.UnixMilli(1_763_000_000_123)
	if got := newV7(now).Time(); !got.Equal(now) {
		t.Errorf("Time() = %v, want %v", got, now)
	}
}

func TestSortsByCreationTime(t *testing.T) {
	a := newV7(time.UnixMilli(1000)).String()
	b := newV7(time.UnixMilli(1001)).String()
	if !(a < b) {
		t.Errorf("%s should sort before %s", a, b)
	}
}

func TestParseRoundTrip(t *testing.T) {
	u := NewV7()
	p, err := Parse(u.String())
	if err != nil || p != u {
		t.Fatalf("Parse(%s) = %v, %v", u, p, err)
	}
}

func TestParseRejects(t *testing.T) {
	for _, s := range []string{
		"",
		"not-a-uuid",
		"0190f0f0-0000-7000-8000-00000000000",   // short
		"0190f0f0x0000-7000-8000-000000000000",  // bad separator
		"0190f0f0-0000-7000-8000-00000000000g",  // bad hex
		"0190f0f0-0000-7000-8000-0000000000000", // long
	} {
		if _, err := Parse(s); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", s)
		}
	}
}

func TestJSON(t *testing.T) {
	type doc struct {
		ID UUID `json:"id"`
	}
	in := doc{ID: NewV7()}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out doc
	if err := json.Unmarshal(b, &out); err != nil || out != in {
		t.Fatalf("round trip: %s -> %+v, %v", b, out, err)
	}
}
