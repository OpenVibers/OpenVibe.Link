//go:build !windows

package worker

import (
	"fmt"
	"io"
)

// SandboxMain: the job sandbox command is Windows' (worker_windows.go); elsewhere the worker confines jobs itself.
func SandboxMain(_ []string, stderr io.Writer) int {
	fmt.Fprintln(stderr, "job-sandbox runs on Windows only")
	return 2
}
