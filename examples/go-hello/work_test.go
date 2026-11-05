package hello

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

// TestWork stands in for a slow test suite: 8 parallel CPU-bound subtests,
// so hosted vs local timings reflect real compute (see scripts/bench.sh).
func TestWork(t *testing.T) {
	for i := range 8 {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			t.Parallel()
			sum := sha256.Sum256([]byte{byte(i)})
			for range 150_000_000 {
				sum = sha256.Sum256(sum[:])
			}
			if sum == ([32]byte{}) {
				t.Fatal("zero hash")
			}
		})
	}
}
