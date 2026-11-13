//go:build !linux

package main

import "errors"

// The pipe (hk-pipe, trunk/pipectl) runs on Linux only: SOCK_SEQPACKET,
// recvmmsg and the sockets it is given by SCM_RIGHTS.
var errPipeLinux = errors.New("EDGE_TRUNK_PIPE: the entry over hk-pipe runs on Linux only")

func runPipeGeneration(Config) error { return errPipeLinux }

func checkPipe(Config) error { return errPipeLinux }
