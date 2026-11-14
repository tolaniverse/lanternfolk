package worldspike

import "testing"

func TestPanUsesTotalTranslation(t *testing.T) {
	base := Camera{X: 40, Y: -12}
	first := Pan(base, 10, 5)
	second := Pan(base, 20, 8)
	if first != (Camera{50, -7}) || second != (Camera{60, -4}) {
		t.Fatalf("first=%+v second=%+v", first, second)
	}
}
