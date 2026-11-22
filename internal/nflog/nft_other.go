//go:build !linux

package nflog

func runNFT(script string) (string, error) { return "", errNotLinux }
