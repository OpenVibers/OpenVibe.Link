//go:build !windows

package main

import "syscall"

// previewDetachAttr starts the background preview in its own session, so it outlives the shell that asked for it.
func previewDetachAttr() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
