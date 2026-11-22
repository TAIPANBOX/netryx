package nflog

// Gap counts the copies the kernel numbered but never delivered.
//
// The socket says ENOBUFS once for a whole burst of loss and never says how
// many packets went with it. The group's own sequence number does: a jump of
// more than one between consecutive copies is exactly that many missing, and
// that number is the difference between a dashboard that is quiet because the
// house is quiet and one that is quiet because it could not keep up.
type Gap struct {
	last uint32
	have bool
	Lost uint64
}

// Saw folds one copy's sequence number in and returns how many were missing
// before it.
func (g *Gap) Saw(seq uint32, ok bool) uint64 {
	if !ok {
		return 0 // this kernel does not number them; nothing can be said
	}
	if !g.have {
		g.last, g.have = seq, true
		return 0
	}
	// Unsigned arithmetic wraps, which is what makes this correct across the
	// counter's own wrap at 2^32 rather than reporting four billion losses
	// once a week on a busy router.
	d := seq - g.last
	g.last = seq
	switch {
	case d == 1:
		return 0
	case d == 0:
		return 0 // the same copy twice is not a loss
	case d > 1<<31:
		// Far enough backwards to be a reorder or a restarted counter rather
		// than a forward jump. Nothing provable, so nothing counted.
		return 0
	default:
		g.Lost += uint64(d - 1)
		return uint64(d - 1)
	}
}
