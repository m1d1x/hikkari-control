//go:build !linux

package main

// tcpInfo: the entry runs on Linux; elsewhere (a developer's mac) the
// client's TCP is not read.
func tcpInfo(uintptr) (tcpSample, bool) { return tcpSample{}, false }
