package hashchain

import "testing"

func TestComputeDeterministic(t *testing.T) {
	a := Compute("prev", []byte(`{"k":"v"}`))
	b := Compute("prev", []byte(`{"k":"v"}`))
	if a != b {
		t.Fatalf("expected determinism, got %s vs %s", a, b)
	}
}

func TestComputeChainsViaPrev(t *testing.T) {
	h1 := Compute("", []byte(`{"i":1}`))
	h2 := Compute(h1, []byte(`{"i":2}`))
	h2Different := Compute("", []byte(`{"i":2}`))
	if h2 == h2Different {
		t.Fatal("hash should depend on prev_hash; got identical hashes")
	}
}

func TestComputeAvoidsPrefixCollision(t *testing.T) {
	// Without the unit-separator, Compute("ab", "c") would equal Compute("a", "bc").
	// The 0x1f separator must prevent that.
	a := Compute("ab", []byte("c"))
	b := Compute("a", []byte("bc"))
	if a == b {
		t.Fatal("prefix collision: separator missing or ineffective")
	}
}
