//go:build linux

package neigh

import (
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Dump asks the kernel for the whole neighbour table once. A watcher alone
// would only learn about devices that change state after it started.
func Dump() ([]Entry, error) {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE)
	if err != nil {
		return nil, fmt.Errorf("netlink socket: %w", err)
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	req := DumpRequest(uint32(os.Getpid()), 1)
	if err := unix.Sendto(fd, req, 0, &unix.SockaddrNetlink{Family: unix.AF_NETLINK}); err != nil {
		return nil, err
	}
	return readUntilDone(fd)
}

// Watch subscribes to neighbour changes and sends each one on the channel
// until the socket is closed. It is the cheap half: a few messages a second in
// a house, and no polling.
func Watch(out chan<- Entry) error {
	fd, err := unix.Socket(unix.AF_NETLINK, unix.SOCK_RAW, unix.NETLINK_ROUTE)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	if err := unix.Bind(fd, &unix.SockaddrNetlink{
		Family: unix.AF_NETLINK,
		Groups: 1 << (unix.RTNLGRP_NEIGH - 1),
	}); err != nil {
		return err
	}
	buf := make([]byte, 64*1024)
	for {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return err
		}
		for _, e := range parseStream(buf[:n]) {
			out <- e
		}
	}
}

func readUntilDone(fd int) ([]Entry, error) {
	var all []Entry
	buf := make([]byte, 64*1024)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		n, _, err := unix.Recvfrom(fd, buf, 0)
		if err != nil {
			return all, err
		}
		entries, done := parseStreamDone(buf[:n])
		all = append(all, entries...)
		if done {
			return all, nil
		}
	}
	return all, nil
}

func parseStream(b []byte) []Entry {
	e, _ := parseStreamDone(b)
	return e
}

// parseStreamDone walks the netlink messages in one datagram.
func parseStreamDone(b []byte) ([]Entry, bool) {
	le := hostEndian()
	var out []Entry
	for len(b) >= 16 {
		l := int(le.Uint32(b[0:4]))
		typ := le.Uint16(b[4:6])
		if l < 16 || l > len(b) {
			break
		}
		switch typ {
		case unix.NLMSG_DONE:
			return out, true
		case unix.NLMSG_ERROR:
			return out, true
		}
		if e, ok := ParseMessage(typ, b[16:l]); ok {
			out = append(out, e)
		}
		b = b[(l+3)&^3:]
	}
	return out, false
}
