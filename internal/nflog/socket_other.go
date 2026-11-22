//go:build !linux

package nflog

import "errors"

var errNotLinux = errors.New("the packet log is a Linux facility: build for the router")

type Reader struct{}

func Open(group uint16, snaplen uint32) (*Reader, error) { return nil, errNotLinux }
func (r *Reader) Close() error                           { return errNotLinux }
func (r *Reader) Receive(buf []byte) ([]Packet, error)   { return nil, errNotLinux }
