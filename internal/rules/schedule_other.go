//go:build !linux

package rules

// KernelEast is 0 off Linux: there is no kernel to ask.
func KernelEast() (int, error) { return 0, nil }
