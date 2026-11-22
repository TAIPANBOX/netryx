//go:build !linux

package neigh

import "errors"

var errNotLinux = errors.New("the neighbour table is a Linux facility: build for the router")

// Dump exists so the parser and the registry can be built and tested anywhere.
func Dump() ([]Entry, error) { return nil, errNotLinux }

// Watch likewise.
func Watch(out chan<- Entry) error { return errNotLinux }
