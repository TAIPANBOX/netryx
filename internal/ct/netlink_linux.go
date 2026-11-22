//go:build linux

package ct

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

// multicast groups, from nfnetlink_compat.h
const (
	grpConntrackNew     = 1
	grpConntrackUpdate  = 2
	grpConntrackDestroy = 3
)

// Dump asks for the whole table once, so a watcher does not start blind.
func Dump() ([]Event, error) {
	fd, err := open(0)
	if err != nil {
		return nil, err
	}
	defer unix.Close(fd)
	req := DumpRequest(uint32(os.Getpid()), 1)
	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	var all []Event
	buf := make([]byte, 64*1024)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return all, err
		}
		events, done, nlErr := walk(buf[:n])
		all = append(all, events...)
		if nlErr != nil {
			return all, nlErr
		}
		if done {
			return all, nil
		}
	}
}

// Watch streams NEW and DESTROY events until the socket errors. Losing events
// is possible under load, and the caller is told rather than left to assume:
// an overflow returns ENOBUFS, which is reported as a dropped count.
func Watch(out chan<- Event, dropped *uint64) error {
	groups := uint32(1<<(grpConntrackNew-1) | 1<<(grpConntrackDestroy-1))
	fd, err := open(groups)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	// A busy house can produce thousands of events a second; a small buffer
	// turns that into silent loss.
	_ = unix.SetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_RCVBUFFORCE, 8<<20)
	buf := make([]byte, 64*1024)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err == unix.ENOBUFS {
			if dropped != nil {
				// Read by the service from another goroutine.
				atomic.AddUint64(dropped, 1)
			}
			continue
		}
		if err != nil {
			return err
		}
		events, _, _ := walk(buf[:n])
		for _, e := range events {
			out <- e
		}
	}
}

// delSeq numbers the delete requests; the kernel echoes it in the answer.
var delSeq uint32

// Delete ends the tracked connection whose original tuple is orig. A
// connection that is already gone is not an error: the cut has happened.
func Delete(orig Tuple) error { return del(orig, false) }

// DeleteReply ends the connection whose reply tuple is reply: a connection
// opened from outside is known on the device's side only by its reply.
func DeleteReply(reply Tuple) error { return del(reply, true) }

func del(t Tuple, reply bool) error {
	req, err := DeleteRequest(uint32(os.Getpid()), atomic.AddUint32(&delSeq, 1), t, reply)
	if err != nil {
		return err
	}
	fd, err := open(0)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	// The answer is one message; a kernel that never answers must not hold
	// the caller.
	_ = unix.SetsockoptTimeval(fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO, &unix.Timeval{Sec: 2})
	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return err
	}
	buf := make([]byte, 8192)
	n, _, err := unix.Recvfrom(fd, buf, 0)
	if err != nil {
		return fmt.Errorf("conntrack delete: no answer: %w", err)
	}
	_, _, nlErr := walk(buf[:n])
	if errors.Is(nlErr, unix.ENOENT) {
		return nil
	}
	return nlErr
}

func open(groups uint32) (int, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_NETFILTER)
	if err != nil {
		return -1, fmt.Errorf("netfilter netlink socket (needs CAP_NET_ADMIN): %w", err)
	}
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK, Groups: groups}); err != nil {
		unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// walk splits a datagram into messages and parses the ones that are events.
// A netlink error is returned rather than swallowed: "no events" and "the
// kernel refused the request" look identical otherwise, and the second one is
// usually a missing module.
func walk(b []byte) ([]Event, bool, error) {
	le := host()
	var out []Event
	for len(b) >= 16 {
		l := int(le.Uint32(b[0:4]))
		typ := le.Uint16(b[4:6])
		if l < 16 || l > len(b) {
			break
		}
		switch typ {
		case unix.NLMSG_DONE:
			return out, true, nil
		case unix.NLMSG_ERROR:
			if l >= 20 {
				if code := int32(le.Uint32(b[16:20])); code != 0 {
					err := unix.Errno(-code)
					if err == unix.EOPNOTSUPP || err == unix.EPROTONOSUPPORT {
						return out, true, fmt.Errorf("conntrack netlink refused the request (%w): the kmod-nf-conntrack-netlink module is probably not loaded", err)
					}
					return out, true, fmt.Errorf("netlink: %w", err)
				}
			}
			return out, true, nil
		}
		if e, ok := ParseEvent(typ, b[16:l]); ok {
			out = append(out, e)
		}
		b = b[(l+3)&^3:]
	}
	return out, false, nil
}

func host() binary.ByteOrder {
	var x uint16 = 1
	if *(*byte)(unsafe.Pointer(&x)) == 1 {
		return binary.LittleEndian
	}
	return binary.BigEndian
}
