//go:build windows

package main

import "syscall"

// previewDetachAttr starts the background preview with no console and in its own process group, so it outlives the
// shell that asked for it (DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP).
func previewDetachAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{CreationFlags: 0x00000008 | 0x00000200, HideWindow: true}
}
