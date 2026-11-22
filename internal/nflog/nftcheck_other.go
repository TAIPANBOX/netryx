//go:build !linux

package nflog

func checkNFT(script string) (string, error) { return "", errNotLinux }
