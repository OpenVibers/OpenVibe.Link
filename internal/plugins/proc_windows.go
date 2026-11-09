//go:build windows

package plugins

import (
	"os/exec"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// setProcAttr is empty on Windows: a process can join a Job Object only once it is running, so the tree is set up
// after Start (startProcessTree).
func setProcAttr(cmd *exec.Cmd) {}

// processTree is the Job Object that holds one plugin process and every process it starts. Windows has no process
// group for this: TerminateProcess ends only the direct child, so a plugin that starts its own helper (a stdio
// relay's child, speech, a camera helper) would leave it running when the Link kills or restarts the plugin. The
// job carries JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE, so closing the last handle also ends everything in it when the
// Link exits or crashes and Windows closes its handles.
type processTree struct {
	cmd *exec.Cmd
	mu  sync.Mutex
	job windows.Handle // 0 once kill/close took it, or when no job could be made
}

// startProcessTree creates the job and puts the running plugin in it. A process the plugin starts before this call
// would be outside the job; the call follows Start at once and the plugin is not sent hello until after it, so the
// gap is the few instructions between the two.
func startProcessTree(cmd *exec.Cmd) (*processTree, error) {
	t := &processTree{cmd: cmd}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return t, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{
		BasicLimitInformation: windows.JOBOBJECT_BASIC_LIMIT_INFORMATION{
			LimitFlags: windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE,
		},
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return t, err
	}
	// Assigning needs PROCESS_SET_QUOTA and PROCESS_TERMINATE on the process, not just its pid.
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return t, err
	}
	err = windows.AssignProcessToJobObject(job, proc)
	_ = windows.CloseHandle(proc)
	if err != nil {
		_ = windows.CloseHandle(job)
		return t, err
	}
	t.mu.Lock()
	t.job = job
	t.mu.Unlock()
	return t, nil
}

// kill ends the whole tree: TerminateJobObject for every process in the job, then the plugin itself in case no job
// could be made.
func (t *processTree) kill() {
	if job := t.takeJob(); job != 0 {
		_ = windows.TerminateJobObject(job, 1)
		_ = windows.CloseHandle(job)
	}
	if t.cmd.Process != nil {
		_ = t.cmd.Process.Kill()
	}
}

// close releases the job once the plugin has exited. KILL_ON_JOB_CLOSE ends anything the plugin left behind; a
// restart starts a new process and so a new job.
func (t *processTree) close() {
	if job := t.takeJob(); job != 0 {
		_ = windows.CloseHandle(job)
	}
}

func (t *processTree) takeJob() windows.Handle {
	t.mu.Lock()
	defer t.mu.Unlock()
	job := t.job
	t.job = 0
	return job
}
