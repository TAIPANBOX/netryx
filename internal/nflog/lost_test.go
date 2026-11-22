package nflog

import "testing"

// The number this produces is the difference between "the house is quiet" and
// "the monitor could not keep up", and nothing else in the system can tell
// those apart.
func TestLostCopiesAreCountedExactly(t *testing.T) {
	var g Gap
	if n := g.Saw(100, true); n != 0 || g.Lost != 0 {
		t.Fatalf("the first copy establishes the count, it is not a loss: %d", n)
	}
	if n := g.Saw(101, true); n != 0 {
		t.Fatalf("consecutive copies lose nothing: %d", n)
	}
	if n := g.Saw(105, true); n != 3 || g.Lost != 3 {
		t.Fatalf("a jump of four means three were missed: %d %d", n, g.Lost)
	}

	// The kernel's counter is 32 bits and wraps. A router that has been up a
	// week must not suddenly report four billion losses.
	var w Gap
	w.Saw(0xfffffffe, true)
	if n := w.Saw(1, true); n != 2 || w.Lost != 2 {
		t.Fatalf("across the wrap the arithmetic must still be exact: %d %d", n, w.Lost)
	}

	// Backwards is a reorder or a counter that restarted. Neither is provable
	// loss, and inventing one would make the number worthless.
	var b Gap
	b.Saw(500, true)
	if n := b.Saw(400, true); n != 0 || b.Lost != 0 {
		t.Fatalf("a backwards jump proves nothing: %d %d", n, b.Lost)
	}
	if n := b.Saw(400, true); n != 0 {
		t.Fatalf("the same number twice is not a loss: %d", n)
	}

	// A kernel that does not number the copies must produce no number at all
	// rather than a stream of imagined losses.
	var none Gap
	none.Saw(7, false)
	if n := none.Saw(9, false); n != 0 || none.Lost != 0 {
		t.Fatalf("without sequence numbers nothing can be said: %d %d", n, none.Lost)
	}
}
