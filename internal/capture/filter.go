// Package capture takes off an interface only the packets worth reading and
// leaves the rest in the kernel.
//
// A home router cannot hand every packet of a gigabit line to a userspace
// program, and it does not need to: the flow counters come from the kernel's
// own connection table, and the only packets that have to be READ are the
// handshakes that carry a name. So the filter runs in the kernel and passes
// two things: a TLS handshake record, and a QUIC Initial on 443. Bulk traffic
// never wakes the process.
package capture

import "golang.org/x/net/bpf"

// offsets, from the start of the Ethernet frame
const (
	ethType  = 12
	ethLen   = 14
	v4Proto  = ethLen + 9
	v4Frag   = ethLen + 6
	v6Next   = ethLen + 6
	v6TCPOff = ethLen + 40 + 12 // TCP data-offset byte
	v6Hdrs   = ethLen + 40      // where the transport header starts
	v6UDPDst = ethLen + 40 + 2
	v6UDPPay = ethLen + 40 + 8
)

const (
	typeIPv4 = 0x0800
	typeIPv6 = 0x86dd
	protoTCP = 6
	protoUDP = 17
	tlsHands = 0x16 // TLS record type: handshake
	quicLong = 0xc0 // QUIC long header (an Initial is one)
	snapLen  = 262144
)

// Filter returns the assembled program. The instructions are written with
// labels and resolved below, because hand-counting jump offsets is how these
// filters get silently wrong.
func Filter() []bpf.Instruction {
	a := &asm{labels: map[string]int{}}
	a.add(bpf.LoadAbsolute{Off: ethType, Size: 2})
	a.jump(typeIPv4, "v4", "")
	a.jump(typeIPv6, "v6", "reject")

	a.mark("v4")
	a.add(bpf.LoadAbsolute{Off: v4Proto, Size: 1})
	a.jump(protoTCP, "v4tcp", "")
	a.jump(protoUDP, "v4udp", "reject")

	a.mark("v4tcp")
	a.add(bpf.LoadAbsolute{Off: v4Frag, Size: 2})
	a.jumpSet(0x1fff, "reject", "") // a fragment carries no usable header
	a.add(bpf.LoadMemShift{Off: ethLen})
	a.add(bpf.LoadIndirect{Off: ethLen + 12, Size: 1}) // TCP data-offset byte
	a.add(bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: 0xf0})
	a.add(bpf.ALUOpConstant{Op: bpf.ALUOpShiftRight, Val: 2}) // header length in bytes
	a.add(bpf.ALUOpX{Op: bpf.ALUOpAdd})                       // + IP header length
	a.add(bpf.ALUOpConstant{Op: bpf.ALUOpAdd, Val: ethLen})
	a.add(bpf.TAX{})
	a.add(bpf.LoadIndirect{Off: 0, Size: 1}) // first payload byte
	a.jump(tlsHands, "accept", "reject")

	a.mark("v4udp")
	a.add(bpf.LoadMemShift{Off: ethLen})
	a.add(bpf.LoadIndirect{Off: ethLen + 2, Size: 2}) // destination port
	a.jump(443, "", "reject")
	a.add(bpf.LoadIndirect{Off: ethLen + 8, Size: 1}) // first UDP payload byte
	a.add(bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: 0xf0})
	a.jump(quicLong, "accept", "reject")

	a.mark("v6")
	a.add(bpf.LoadAbsolute{Off: v6Next, Size: 1})
	a.jump(protoTCP, "v6tcp", "")
	a.jump(protoUDP, "v6udp", "reject")

	a.mark("v6tcp")
	a.add(bpf.LoadAbsolute{Off: v6TCPOff, Size: 1})
	a.add(bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: 0xf0})
	a.add(bpf.ALUOpConstant{Op: bpf.ALUOpShiftRight, Val: 2})
	a.add(bpf.ALUOpConstant{Op: bpf.ALUOpAdd, Val: v6Hdrs})
	a.add(bpf.TAX{})
	a.add(bpf.LoadIndirect{Off: 0, Size: 1})
	a.jump(tlsHands, "accept", "reject")

	a.mark("v6udp")
	a.add(bpf.LoadAbsolute{Off: v6UDPDst, Size: 2})
	a.jump(443, "", "reject")
	a.add(bpf.LoadAbsolute{Off: v6UDPPay, Size: 1})
	a.add(bpf.ALUOpConstant{Op: bpf.ALUOpAnd, Val: 0xf0})
	a.jump(quicLong, "accept", "reject")

	a.mark("accept")
	a.add(bpf.RetConstant{Val: snapLen})
	a.mark("reject")
	a.add(bpf.RetConstant{Val: 0})
	return a.resolve()
}

// --- a very small assembler, so the filter above can be read ---

type pending struct {
	at            int
	trueL, falseL string
}

type asm struct {
	ins    []bpf.Instruction
	labels map[string]int
	pend   []pending
}

func (a *asm) add(i bpf.Instruction) { a.ins = append(a.ins, i) }
func (a *asm) mark(name string)      { a.labels[name] = len(a.ins) }
func (a *asm) jump(val uint32, t, f string) {
	a.pend = append(a.pend, pending{at: len(a.ins), trueL: t, falseL: f})
	a.ins = append(a.ins, bpf.JumpIf{Cond: bpf.JumpEqual, Val: val})
}
func (a *asm) jumpSet(val uint32, t, f string) {
	a.pend = append(a.pend, pending{at: len(a.ins), trueL: t, falseL: f})
	a.ins = append(a.ins, bpf.JumpIf{Cond: bpf.JumpBitsSet, Val: val})
}

// resolve turns label names into the relative skips the kernel wants. An empty
// label means "fall through", which is skip 0.
func (a *asm) resolve() []bpf.Instruction {
	out := make([]bpf.Instruction, len(a.ins))
	copy(out, a.ins)
	for _, p := range a.pend {
		j := out[p.at].(bpf.JumpIf)
		if p.trueL != "" {
			j.SkipTrue = uint8(a.labels[p.trueL] - p.at - 1)
		}
		if p.falseL != "" {
			j.SkipFalse = uint8(a.labels[p.falseL] - p.at - 1)
		}
		out[p.at] = j
	}
	return out
}
