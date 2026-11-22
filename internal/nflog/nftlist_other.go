//go:build !linux

package nflog

func listNFT(table string) (string, error) { return "", errNotLinux }
