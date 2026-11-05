package hello

import "testing"

// To see a red check, change want to 5 and push.
func TestAdd(t *testing.T) {
	if got, want := Add(2, 2), 4; got != want {
		t.Fatalf("Add(2, 2) = %d, want %d", got, want)
	}
}
