//go:build !darwin

package main

// procArgs is only needed on macOS; Linux reads /proc/<pid>/environ.
func procArgs(pid int) [][]byte { return nil }
