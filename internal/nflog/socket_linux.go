//go:build linux

package nflog

import (
	"encoding/binary"
	"fmt"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// configuration attributes and commands
const (
	cfgCmd      = 1
	cfgMode     = 2
	cfgNlBufSiz = 3
	cfgTimeout  = 4
	cfgQThresh  = 5
	cfgFlags    = 6

	cmdBind     = 1
	cmdPFBind   = 3
	cmdPFUnbind = 4

	copyPacket = 2

	// flagConntrack makes the kernel attach the connection to every copy,
	// which is what lets a captured handshake name its device without any
	// join against a separate table.
	flagConntrack = 0x0004
	// flagSeq asks the kernel to number the copies it sends for this group.
	// Without it a lost copy is invisible: the socket reports one ENOBUFS for
	// a whole burst and never says how many packets went with it, so a
	// dashboard under load quietly under-reports and looks calm.
	flagSeq = 0x0001
)

// Reader receives the packets the kernel selected.
type Reader struct {
	fd    int
	group uint16
	seq   uint32
	// pending holds packet copies that arrived while a configuration
	// acknowledgement was being waited for. The group goes live the moment it
	// is bound, so copies and acks share this socket from then on; dropping
	// them would lose the first handshakes of every run.
	pending [][]byte
}

// Open binds to a log group and asks for whole packets with their connection
// attached. Snaplen caps how much of each packet the kernel copies.
func Open(group uint16, snaplen uint32) (*Reader, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_NETFILTER)
	if err != nil {
		return nil, fmt.Errorf("netfilter netlink socket (needs CAP_NET_ADMIN): %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		unix.Close(fd)
		return nil, err
	}
	r := &Reader{fd: fd, group: group}
	// A house at full tilt can produce thousands of copies a second; a small
	// buffer turns that into loss nobody is told about.
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, 8<<20)

	// Taking the protocol family is what the library does; both families are
	// covered by AF_INET here, and the unbind first is the documented dance.
	if err := r.config(unix.AF_INET, cmdPFUnbind, 0, nil); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if err := r.config(unix.AF_INET, cmdPFBind, 0, nil); err != nil {
		unix.Close(fd)
		return nil, err
	}
	// One attribute per message, the way libnetfilter_log does it: the kernel
	// takes the bind, then the copy mode, then the flags, and rejects a
	// message that carries them together.
	if err := r.config(unix.AF_UNSPEC, cmdBind, group, nil); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("binding log group %d: %w", group, err)
	}
	// struct nfulnl_msg_config_mode is six bytes: a big-endian range, the
	// mode, and a byte of padding. Five bytes is what ERANGE looks like.
	mode := make([]byte, 6)
	binary.BigEndian.PutUint32(mode[0:4], snaplen)
	mode[4] = copyPacket
	if err := r.config(unix.AF_UNSPEC, 0, group, [][2]any{{uint16(cfgMode), any(mode)}}); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("copy mode on group %d: %w", group, err)
	}
	flags := make([]byte, 2)
	binary.BigEndian.PutUint16(flags, flagConntrack|flagSeq)
	// The first attempt can come back EAGAIN while the kernel loads
	// nf_conntrack_netlink on demand; it asks to be retried rather than
	// failing, and reporting that as "not supported" sends people installing
	// packages they already have.
	err = r.config(unix.AF_UNSPEC, 0, group, [][2]any{{uint16(cfgFlags), any(flags)}})
	if err == unix.EAGAIN {
		time.Sleep(200 * time.Millisecond)
		err = r.config(unix.AF_UNSPEC, 0, group, [][2]any{{uint16(cfgFlags), any(flags)}})
	}
	if err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("attaching the connection on group %d: %w", group, err)
	}
	return r, nil
}

func (r *Reader) Close() error { return unix.Close(r.fd) }

// Receive returns the copies that arrived, blocking only when there are none
// held over from the configuration exchange.
func (r *Reader) Receive(buf []byte) ([]Packet, error) {
	if len(r.pending) > 0 {
		var out []Packet
		for _, m := range r.pending {
			if p, ok := ParsePacket(hostOrderU16(m[4:6]), m[16:]); ok {
				out = append(out, p)
			}
		}
		r.pending = nil
		if len(out) > 0 {
			return out, nil
		}
	}
	return r.receive(buf)
}

func hostOrderU16(b []byte) uint16 { return host().Uint16(b) }

func (r *Reader) receive(buf []byte) ([]Packet, error) {
	n, _, err := unix.Recvfrom(r.fd, buf, 0)
	if err != nil {
		return nil, err
	}
	le := host()
	var out []Packet
	b := buf[:n]
	for len(b) >= 16 {
		l := int(le.Uint32(b[0:4]))
		typ := le.Uint16(b[4:6])
		if l < 16 || l > len(b) {
			break
		}
		if typ == unix.NLMSG_ERROR {
			if l >= 20 {
				if code := int32(le.Uint32(b[16:20])); code != 0 {
					return out, fmt.Errorf("netlink: %w", unix.Errno(-code))
				}
			}
		} else if p, ok := ParsePacket(typ, b[16:l]); ok {
			out = append(out, p)
		}
		b = b[(l+3)&^3:]
	}
	return out, nil
}

// config sends one NFULNL_MSG_CONFIG message: either a command, or the
// attributes given, never both.
func (r *Reader) config(family uint8, cmd uint8, group uint16, extra [][2]any) error {
	le := host()
	var attrs []byte
	if cmd != 0 {
		attrs = append(attrs, attrOut(cfgCmd, []byte{cmd})...)
	}
	for _, kv := range extra {
		attrs = append(attrs, attrOut(kv[0].(uint16), kv[1].([]byte))...)
	}
	msg := make([]byte, 20+len(attrs))
	le.PutUint32(msg[0:4], uint32(len(msg)))
	le.PutUint16(msg[4:6], uint16(subsysULOG)<<8|msgConfig)
	le.PutUint16(msg[6:8], unix.NLM_F_REQUEST|unix.NLM_F_ACK)
	r.seq++
	seq := r.seq
	le.PutUint32(msg[8:12], seq)
	le.PutUint32(msg[12:16], uint32(os.Getpid()))
	msg[16] = family
	msg[17] = 0 // nfnetlink version
	binary.BigEndian.PutUint16(msg[18:20], group)
	copy(msg[20:], attrs)
	if err := unix.Sendto(r.fd, msg, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	// Read until the acknowledgement for THIS message. A packet copy that
	// arrives first is kept rather than mistaken for the answer.
	buf := make([]byte, 64*1024)
	for {
		n, _, err := unix.Recvfrom(r.fd, buf, 0)
		if err != nil {
			return err
		}
		b := buf[:n]
		for len(b) >= 16 {
			l := int(le.Uint32(b[0:4]))
			typ := le.Uint16(b[4:6])
			sq := le.Uint32(b[8:12])
			if l < 16 || l > len(b) {
				break
			}
			switch {
			case typ == unix.NLMSG_ERROR && sq == seq:
				if l >= 20 {
					if code := int32(le.Uint32(b[16:20])); code != 0 {
						return unix.Errno(-code)
					}
				}
				return nil
			case typ>>8 == subsysULOG && typ&0xff == msgPacket:
				cp := make([]byte, l)
				copy(cp, b[:l])
				r.pending = append(r.pending, cp)
			}
			b = b[(l+3)&^3:]
		}
	}
}

func attrOut(typ uint16, val []byte) []byte {
	l := 4 + len(val)
	b := make([]byte, (l+3)&^3)
	binary.LittleEndian.PutUint16(b[0:2], uint16(l))
	binary.LittleEndian.PutUint16(b[2:4], typ)
	copy(b[4:], val)
	return b
}

func host() binary.ByteOrder {
	var x uint16 = 1
	if *(*byte)(unsafe.Pointer(&x)) == 1 {
		return binary.LittleEndian
	}
	return binary.BigEndian
}
