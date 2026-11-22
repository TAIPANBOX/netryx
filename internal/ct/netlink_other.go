//go:build !linux

package ct

import "errors"

var errNotLinux = errors.New("conntrack netlink is a Linux facility: build for the router")

func Dump() ([]Event, error)                        { return nil, errNotLinux }
func Watch(out chan<- Event, dropped *uint64) error { return errNotLinux }
func Delete(orig Tuple) error                       { return errNotLinux }
func DeleteReply(reply Tuple) error                 { return errNotLinux }
