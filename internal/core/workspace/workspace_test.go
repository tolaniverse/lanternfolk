package workspace

import (
	"crypto/sha256"
	"errors"
	"testing"
)

type shaHasher struct{}

func (shaHasher) Hash(a string, salt []byte) []byte {
	s := sha256.Sum256(append(append([]byte{}, salt...), a...))
	return s[:]
}

func gate(answer string) Gate {
	salt := []byte("salt-" + answer)
	return Gate{Kind: "riddle", Prompt: "?", Salt: salt, Hash: shaHasher{}.Hash(answer, salt)}
}

func TestCheckGates(t *testing.T) {
	gates := []Gate{gate("open sesame"), gate("mellon")}
	cases := []struct {
		name    string
		answers []string
		ok      bool
	}{
		{"all right", []string{"open sesame", "mellon"}, true},
		{"second wrong", []string{"open sesame", "friend"}, false},
		{"first wrong", []string{"close sesame", "mellon"}, false},
		{"too few", []string{"open sesame"}, false},
		{"too many", []string{"open sesame", "mellon", "x"}, false},
	}
	for _, c := range cases {
		err := CheckGates(gates, c.answers, shaHasher{})
		if c.ok && err != nil {
			t.Errorf("%s: unexpected %v", c.name, err)
		}
		if !c.ok && !errors.Is(err, ErrGateFailed) {
			t.Errorf("%s: err = %v, want ErrGateFailed", c.name, err)
		}
	}
	if err := CheckGates(nil, nil, shaHasher{}); err != nil {
		t.Errorf("no gates should pass: %v", err)
	}
}

func TestRoles(t *testing.T) {
	if !Owner.CanPost() || !Member.CanPost() || Role("guest").CanPost() {
		t.Error("CanPost rules")
	}
}
