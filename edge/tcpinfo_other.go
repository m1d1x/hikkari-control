//go:build !linux && !darwin

package main

func tcpInfo(uintptr) (tcpSample, bool) { return tcpSample{}, false }
