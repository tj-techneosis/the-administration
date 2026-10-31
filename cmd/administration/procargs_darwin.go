package main

import (
	"bytes"

	"golang.org/x/sys/unix"
)

// procArgs is what the kernel keeps of a process's start: its path, its
// arguments and its environment, NUL-separated, read the way ps reads them.
// It's there even when the process has written a new title over its
// command line (a Next.js server does), which `ps -E` then no longer shows.
func procArgs(pid int) [][]byte {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		return nil
	}
	return bytes.Split(b[4:], []byte{0})
}
