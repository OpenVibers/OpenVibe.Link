//go:build windows

package worker

// The job worker on Windows (plan 2026-11-13 §AD.13 (2); the owner, 11-15: tools on "windows and mac" too). Windows
// has no namespaces, so like macOS it runs one kind of job only: the skill runner (openvibe-skill-runner, class
// function). Two layers confine it:
//
//   - the whole job (the runner and everything it starts) is one Job Object: its memory (job memory) and CPU time
//     (per-job user time) limits, a cap on processes, and kill-on-close, so nothing outlives the job and a kill ends
//     every process in it;
//   - the tool itself, the untrusted code, runs in an AppContainer (Windows' app sandbox, the one browsers run their
//     renderers in) with no capabilities: no network at all (an AppContainer without internetClient or privateNetwork
//     reaches nothing, loopback included), and no file it may read or write but its job folder and what every app may
//     read (the system, Program Files). The runner starts it through this binary (`openvibe-link job-sandbox`), the
//     command named in OPENVIBE_CONFINE; Node's permission model still applies inside.
//
// Windows hands a child no fd 3, so the runner writes its result to OPENVIBE_RESULT_FILE, a file outside the job
// folder (the tool cannot reach it) that the worker reads once the runner exits. A job naming a network, a host egress
// other than none, a runner too old to confine its tool, and any other function are refused.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/OpenVibers/OpenVibe.Link/internal/config"
	"github.com/OpenVibers/OpenVibe.Link/internal/protocol"
)

// errNotLinux: what only Linux has (desks: desk_other.go).
var errNotLinux = errors.New("this runs on Linux only")

// skillRunnerName is the one function this worker runs (internal/skills Name; not imported, to keep the worker free of it).
const skillRunnerName = "openvibe-skill-runner"

// confineMarker is in every skill runner that starts its tool through OPENVIBE_CONFINE (Link v0.71.0's): an older
// runner would start the tool outside the AppContainer, so the worker refuses it until `skills on` (or the Link's own
// refresh) writes the current one.
const confineMarker = "OPENVIBE_CONFINE"

// appContainerName names the one AppContainer profile every job's tool runs in (its SID is derived from the name).
const appContainerName = "OpenVibe.Link.Job"

// jobProcesses caps the processes in a job: the runner, the launcher, the tool, and the console hosts Windows adds.
const jobProcesses = 16

func workDir(dir string) string { return dir }

func isolate(cmd *exec.Cmd, cfg config.WorkerConfig, p plan) (*sandbox, error) {
	if p.fn.Name != "" && (p.fn.Class != config.ClassFunction || p.fn.Name != skillRunnerName) {
		return nil, errors.New("on Windows the job worker runs the skill runner only")
	}
	if p.egress != "" {
		return nil, errors.New("on Windows a job runs with no network")
	}
	if p.fn.Name != "" {
		if err := runnerConfines(p.fn); err != nil {
			return nil, err
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	sid, err := appContainerSID()
	if err != nil {
		return nil, fmt.Errorf("no AppContainer for the tool: %w", err)
	}
	defer windows.FreeSid(sid)
	if err := grantDir(p.root, sid, windows.GENERIC_ALL); err != nil {
		return nil, fmt.Errorf("the job's folder could not be opened to its sandbox: %w", err)
	}
	res, err := os.CreateTemp("", "openvibe-result-")
	if err != nil {
		return nil, err
	}
	res.Close()
	confine, _ := json.Marshal([]string{exe, SandboxArg, "--"})
	cmd.ExtraFiles = nil
	cmd.Env = windowsEnv(cmd.Env, p.root, string(confine), res.Name())
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return &sandbox{limits: p.limits, result: res.Name()}, nil
}

// runnerConfines reads the runner script the entry names and refuses one that predates OPENVIBE_CONFINE.
func runnerConfines(fn config.FunctionConfig) error {
	if len(fn.Command) < 2 {
		return errors.New("the skill runner entry names no script")
	}
	b, err := os.ReadFile(fn.Command[1])
	if err != nil {
		return fmt.Errorf("the skill runner script: %w", err)
	}
	if !strings.Contains(string(b), confineMarker) {
		return errors.New("this skill runner predates the Windows sandbox; run `openvibe-link skills on` again")
	}
	return nil
}

// windowsEnv is the job's environment on Windows: the job's own (jobEnv) with a Windows PATH, its folder as the
// temporary and profile folders (os.tmpdir() of the runner, where it writes a tool's source), and the two variables
// the runner reads here.
func windowsEnv(env []string, dir, confine, result string) []string {
	sys := os.Getenv("SystemRoot")
	if sys == "" {
		sys = `C:\Windows`
	}
	set := map[string]string{
		"PATH": filepath.Join(sys, "System32") + ";" + sys, "SystemRoot": sys, "TEMP": dir, "TMP": dir,
		"USERPROFILE": dir, "OPENVIBE_CONFINE": confine, "OPENVIBE_RESULT_FILE": result,
	}
	out := make([]string, 0, len(env)+len(set))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := set[k]; ok || k == "TMPDIR" || k == "HOME" {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}

// probe: the host's egress is none, the skill runner is on, and the whole chain holds: a process started the way a
// job's runner is starts its child through OPENVIBE_CONFINE, and that child (in the AppContainer, without Node's
// permission model, so the sandbox alone is tested) writes in its folder but not outside it, reads no file outside
// it, and reaches no listener on loopback; the result comes back through OPENVIBE_RESULT_FILE.
func probe(cfg config.WorkerConfig, isolate isolateFunc) error {
	if cfg.Egress != "" && cfg.Egress != config.EgressNone {
		return errors.New("on Windows the job worker runs with worker.egress none only")
	}
	node := ""
	for _, f := range cfg.Functions {
		if f.Name == skillRunnerName && f.Class == config.ClassFunction && len(f.Command) > 0 {
			node = f.Command[0]
		}
	}
	if node == "" {
		return errors.New("on Windows the job worker runs the skill runner only: turn it on with `openvibe-link skills on`")
	}
	root, err := os.MkdirTemp("", "openvibe-probe-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(root)
	secret := filepath.Join(os.TempDir(), fmt.Sprintf("openvibe-probe-secret-%d", os.Getpid()))
	if err := os.WriteFile(secret, []byte("secret"), 0o600); err != nil {
		return err
	}
	defer os.Remove(secret)
	outside := filepath.Join(os.TempDir(), fmt.Sprintf("openvibe-probe-outside-%d", os.Getpid()))
	defer os.Remove(outside)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	inner := fmt.Sprintf(`const fs = require('fs'), net = require('net'); const out = {};
try { fs.writeFileSync('inside', 'ok'); out.inside = 'ok'; } catch (e) { out.inside = e.code || 'ERR'; }
try { fs.writeFileSync(%q, 'x'); out.outside = 'ALLOWED'; } catch (e) { out.outside = e.code || 'ERR'; }
try { fs.readFileSync(%q); out.secret = 'ALLOWED'; } catch (e) { out.secret = e.code || 'ERR'; }
let sent = false; const done = (v) => { if (sent) return; sent = true; out.net = v; process.stdout.write(JSON.stringify(out)); process.exit(0); };
const s = net.connect(%d, '127.0.0.1'); s.on('connect', () => done('ALLOWED')); s.on('error', (e) => done(e.code || 'ERR'));
setTimeout(() => done('TIMEOUT'), 3000);`, outside, secret, port)
	outer := `const { spawn } = require('child_process'); const fs = require('fs');
const [cmd, ...pre] = JSON.parse(process.env.OPENVIBE_CONFINE);
const c = spawn(cmd, [...pre, process.execPath, '-e', process.argv[1]], { env: {}, stdio: ['ignore', 'pipe', 'pipe'], windowsHide: true });
let o = '', e = ''; c.stdout.on('data', (d) => o += d); c.stderr.on('data', (d) => e += d);
c.on('error', (err) => { fs.writeFileSync(process.env.OPENVIBE_RESULT_FILE, JSON.stringify({ error: err.message })); });
c.on('close', (code) => { fs.writeFileSync(process.env.OPENVIBE_RESULT_FILE, o || JSON.stringify({ code, stderr: e.slice(0, 400) })); });`
	cmd := exec.Command(node, "-e", outer, inner)
	cmd.Dir, cmd.Env = root, []string{"OPENVIBE_JOB_ID=probe"}
	sb, err := isolate(cmd, cfg, plan{root: root})
	if err != nil {
		return err
	}
	defer sb.close()
	if err := cmd.Start(); err != nil {
		return err
	}
	if err := sb.ready(cmd.Process.Pid); err != nil {
		killGroup(cmd.Process)
		_ = cmd.Wait()
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		killGroup(cmd.Process)
		<-done
		return errors.New("a confined process did not finish in 30 s")
	}
	b, _ := os.ReadFile(sb.resultFile())
	var got struct{ Inside, Outside, Secret, Net, Error, Stderr string }
	if err := json.Unmarshal(b, &got); err != nil {
		return fmt.Errorf("a confined process gave no answer: %q", strings.TrimSpace(string(b)))
	}
	switch {
	case got.Error != "" || got.Inside == "":
		return fmt.Errorf("a confined process did not run: %s %s", got.Error, got.Stderr)
	case got.Inside != "ok":
		return fmt.Errorf("a confined process could not write in its folder (%s)", got.Inside)
	case got.Outside == "ALLOWED":
		return errors.New("a confined process wrote outside its folder")
	case got.Secret == "ALLOWED":
		return errors.New("a confined process read a file outside its folder")
	case got.Net == "ALLOWED":
		return errors.New("a confined process reached the network (loopback)")
	}
	if _, err := os.Stat(outside); err == nil {
		return errors.New("a confined process wrote outside its folder")
	}
	return nil
}

// jobObjects maps a running job's runner pid to its Job Object, so killGroup ends the whole job (the launcher and the tool
// hold the runner's output open; killing the runner alone would leave them, and the job, running).
var jobObjects sync.Map // int → windows.Handle

// sandbox is the job's Job Object (made in ready, once the runner is running) and its result file.
type sandbox struct {
	limits protocol.JobLimits
	result string
	mu     sync.Mutex
	job    windows.Handle
	pid    int
}

func (s *sandbox) started() {}

// ready puts the runner in a new Job Object with the job's limits. The runner starts nothing before it has read its
// arguments and written the tool's source, long after this, so the whole tree is in the job.
func (s *sandbox) ready(pid int) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	flags := uint32(windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION | windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS)
	info.BasicLimitInformation.ActiveProcessLimit = jobProcesses
	if s.limits.MemBytes > 0 {
		flags |= windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		info.JobMemoryLimit = uintptr(s.limits.MemBytes)
	}
	if s.limits.CPUMS > 0 {
		flags |= windows.JOB_OBJECT_LIMIT_JOB_TIME
		info.BasicLimitInformation.PerJobUserTimeLimit = s.limits.CPUMS * 10_000 // 100 ns units
	}
	info.BasicLimitInformation.LimitFlags = flags
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	err = windows.AssignProcessToJobObject(job, proc)
	_ = windows.CloseHandle(proc)
	if err != nil {
		_ = windows.CloseHandle(job)
		return err
	}
	s.mu.Lock()
	s.job, s.pid = job, pid
	s.mu.Unlock()
	jobObjects.Store(pid, job)
	return nil
}

func (*sandbox) run() error { return nil }

// basicAccounting is JOBOBJECT_BASIC_ACCOUNTING_INFORMATION (x/sys/windows has no type for it).
type basicAccounting struct {
	TotalUserTime, TotalKernelTime, ThisPeriodTotalUserTime, ThisPeriodTotalKernelTime int64
	TotalPageFaultCount, TotalProcesses, ActiveProcesses, TotalTerminatedProcesses     uint32
}

// oomKilled reports whether the job ran into its memory or its CPU time limit (Windows ends the job itself at the CPU
// limit, so either is the job's limit, not its own failure).
func (s *sandbox) oomKilled() bool {
	s.mu.Lock()
	job := s.job
	s.mu.Unlock()
	if job == 0 {
		return false
	}
	if s.limits.MemBytes > 0 {
		var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
		if windows.QueryInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info)), nil) == nil &&
			int64(info.PeakJobMemoryUsed)+(1<<20) >= s.limits.MemBytes {
			return true
		}
	}
	if s.limits.CPUMS > 0 {
		var acc basicAccounting
		if windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation,
			uintptr(unsafe.Pointer(&acc)), uint32(unsafe.Sizeof(acc)), nil) == nil &&
			acc.TotalUserTime >= s.limits.CPUMS*10_000 {
			return true
		}
	}
	return false
}

// resultFile is where the runner wrote its result (Windows has no fd 3).
func (s *sandbox) resultFile() string { return s.result }

// close ends whatever is left of the job (kill-on-close) and removes the result file.
func (s *sandbox) close() {
	s.mu.Lock()
	job, pid := s.job, s.pid
	s.job = 0
	s.mu.Unlock()
	if job != 0 {
		jobObjects.CompareAndDelete(pid, job)
		_ = windows.CloseHandle(job)
	}
	if s.result != "" {
		_ = os.Remove(s.result)
	}
}

// killGroup ends every process of the job p runs (its Job Object), or p alone before it has one.
func killGroup(p *os.Process) {
	if h, ok := jobObjects.Load(p.Pid); ok {
		_ = windows.TerminateJobObject(h.(windows.Handle), 1)
	}
	_ = p.Kill()
}

// limitCPU: the Job Object carries the CPU limit (ready).
func limitCPU(int, int64) error { return nil }

func pidNS(int) (string, error) { return "", nil }

// sampleProcs: nothing sampled on Windows; the Job Object enforces memory and CPU time itself.
func sampleProcs(int, string) (int64, int64, error) { return 0, 0, nil }

func exitStatus(ps *os.ProcessState) (*int, bool) {
	if c := ps.ExitCode(); c >= 0 {
		return &c, false
	}
	return nil, false
}

func maxRSS(*os.ProcessState) int64 { return 0 }

// ── the AppContainer ──

var (
	userenv                                    = windows.NewLazySystemDLL("userenv.dll")
	procCreateAppContainerProfile              = userenv.NewProc("CreateAppContainerProfile")
	procDeriveAppContainerSidFromContainerName = userenv.NewProc("DeriveAppContainerSidFromAppContainerName")
)

// appContainerSID creates the jobs' AppContainer profile the first time and returns its SID (free with FreeSid).
func appContainerSID() (*windows.SID, error) {
	name, _ := windows.UTF16PtrFromString(appContainerName)
	desc, _ := windows.UTF16PtrFromString("OpenVibe Link jobs: a tool's code, with no network and only its own folder")
	var sid *windows.SID
	hr, _, _ := procCreateAppContainerProfile.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(name)),
		uintptr(unsafe.Pointer(desc)), 0, 0, uintptr(unsafe.Pointer(&sid)))
	if hr == 0 {
		return sid, nil
	}
	// HRESULT_FROM_WIN32(ERROR_ALREADY_EXISTS): the profile is there; its SID follows from the name.
	if uint32(hr) != 0x800700B7 {
		return nil, fmt.Errorf("CreateAppContainerProfile: 0x%08x", uint32(hr))
	}
	hr, _, _ = procDeriveAppContainerSidFromContainerName.Call(uintptr(unsafe.Pointer(name)), uintptr(unsafe.Pointer(&sid)))
	if hr != 0 {
		return nil, fmt.Errorf("DeriveAppContainerSidFromAppContainerName: 0x%08x", uint32(hr))
	}
	return sid, nil
}

// grantDir adds an inheritable allow entry for sid to dir's DACL (kept: the owner's own entries stay as they are).
func grantDir(dir string, sid *windows.SID, mask windows.ACCESS_MASK) error {
	sd, err := windows.GetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	old, _, err := sd.DACL()
	if err != nil {
		return err
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: mask,
		AccessMode:        windows.GRANT_ACCESS,
		Inheritance:       windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_UNKNOWN,
			TrusteeValue: windows.TrusteeValueFromSID(sid),
		},
	}}, old)
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

// containerEnv is the environment block a confined process starts with: this launcher's own (the skill runner starts a
// tool with an empty one, to which Node adds the system's required variables) plus LOCALAPPDATA and APPDATA when they
// are missing, which Windows needs to set up an AppContainer process (it points them at the container's own folders;
// without them CreateProcess fails with ERROR_ENVVAR_NOT_FOUND). UTF-16, sorted, double-NUL terminated.
func containerEnv(base []string) []uint16 {
	have := map[string]bool{}
	for _, kv := range base {
		k, _, _ := strings.Cut(kv, "=")
		have[strings.ToUpper(k)] = true
	}
	env := append([]string(nil), base...)
	for k, id := range map[string]*windows.KNOWNFOLDERID{"LOCALAPPDATA": windows.FOLDERID_LocalAppData, "APPDATA": windows.FOLDERID_RoamingAppData} {
		if have[k] {
			continue
		}
		if p, err := windows.KnownFolderPath(id, 0); err == nil {
			env = append(env, k+"="+p)
		}
	}
	sort.Slice(env, func(i, j int) bool { return strings.ToUpper(env[i]) < strings.ToUpper(env[j]) })
	var block []uint16
	for _, kv := range env {
		if strings.HasPrefix(kv, "=") || strings.IndexByte(kv, 0) >= 0 {
			continue // the drive-letter entries (=C:=C:\...) and anything with a NUL
		}
		u, err := windows.UTF16FromString(kv)
		if err != nil {
			continue
		}
		block = append(block, u...) // ends with its own NUL
	}
	if len(block) == 0 {
		return []uint16{0, 0}
	}
	return append(block, 0)
}

// procThreadAttributeSecurityCapabilities is PROC_THREAD_ATTRIBUTE_SECURITY_CAPABILITIES (not in x/sys/windows).
const procThreadAttributeSecurityCapabilities = 0x00020009

// securityCapabilities is SECURITY_CAPABILITIES: the AppContainer a process starts in, and its capabilities (none).
type securityCapabilities struct {
	AppContainerSid *windows.SID
	Capabilities    *windows.SIDAndAttributes
	CapabilityCount uint32
	Reserved        uint32
}

// SandboxMain is `openvibe-link job-sandbox -- PROGRAM [ARG]...`: it runs PROGRAM in the jobs' AppContainer with no
// capabilities, in a Job Object of its own (so killing this launcher ends it), with this process's standard handles,
// environment and folder, and exits with its exit code. The skill runner starts a tool's code through it
// (OPENVIBE_CONFINE).
func SandboxMain(args []string, stderr io.Writer) int {
	if len(args) < 2 || args[0] != "--" {
		fmt.Fprintln(stderr, "usage: openvibe-link job-sandbox -- PROGRAM [ARG]...")
		return 2
	}
	code, err := runConfined(args[1:])
	if err != nil {
		fmt.Fprintln(stderr, "job-sandbox:", err)
		return 125
	}
	return int(code)
}

func runConfined(argv []string) (uint32, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	sid, err := appContainerSID()
	if err != nil {
		return 0, err
	}
	defer windows.FreeSid(sid)
	prog, err := exec.LookPath(argv[0])
	if err != nil {
		return 0, err
	}
	if prog, err = filepath.Abs(prog); err != nil {
		return 0, err
	}
	// A Node outside Program Files (a version manager's, the user's own) is opened to the container; Program Files and
	// the system already are. Best effort: a folder this user cannot change is left as it is.
	_ = grantDir(filepath.Dir(prog), sid, windows.GENERIC_READ|windows.GENERIC_EXECUTE)

	// The standard handles, inheritable, and only they: the handle list keeps every other handle of this process out.
	var handles []windows.Handle
	si := new(windows.StartupInfoEx)
	si.Cb = uint32(unsafe.Sizeof(*si))
	si.Flags = windows.STARTF_USESTDHANDLES
	for i, n := range []uint32{windows.STD_INPUT_HANDLE, windows.STD_OUTPUT_HANDLE, windows.STD_ERROR_HANDLE} {
		h, err := windows.GetStdHandle(n)
		if err != nil || h == 0 || h == windows.InvalidHandle {
			continue
		}
		if err := windows.SetHandleInformation(h, windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT); err != nil {
			continue
		}
		switch i {
		case 0:
			si.StdInput = h
		case 1:
			si.StdOutput = h
		case 2:
			si.StdErr = h
		}
		seen := false
		for _, x := range handles {
			seen = seen || x == h
		}
		if !seen {
			handles = append(handles, h)
		}
	}
	attrs, err := windows.NewProcThreadAttributeList(2)
	if err != nil {
		return 0, err
	}
	defer attrs.Delete()
	caps := &securityCapabilities{AppContainerSid: sid}
	if err := attrs.Update(procThreadAttributeSecurityCapabilities, unsafe.Pointer(caps), unsafe.Sizeof(*caps)); err != nil {
		return 0, err
	}
	if len(handles) > 0 {
		if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), uintptr(len(handles))*unsafe.Sizeof(handles[0])); err != nil {
			return 0, err
		}
	}
	si.ProcThreadAttributeList = attrs.List()

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(job)
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		return 0, err
	}

	app, _ := windows.UTF16PtrFromString(prog)
	line, _ := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{prog}, argv[1:]...)))
	// Windows reads LOCALAPPDATA and APPDATA from this process as well as from the child's block when it sets up the
	// container, so they are set here too (the runner starts this launcher with an empty environment).
	for k, id := range map[string]*windows.KNOWNFOLDERID{"LOCALAPPDATA": windows.FOLDERID_LocalAppData, "APPDATA": windows.FOLDERID_RoamingAppData} {
		if os.Getenv(k) == "" {
			if p, err := windows.KnownFolderPath(id, 0); err == nil {
				_ = os.Setenv(k, p)
			}
		}
	}
	env := containerEnv(os.Environ())
	var pi windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_SUSPENDED | windows.CREATE_NO_WINDOW | windows.CREATE_UNICODE_ENVIRONMENT)
	if err := windows.CreateProcess(app, line, nil, nil, len(handles) > 0, flags, &env[0], nil, &si.StartupInfo, &pi); err != nil {
		return 0, fmt.Errorf("start %s in the sandbox: %w", prog, err)
	}
	runtime.KeepAlive(caps)
	runtime.KeepAlive(handles)
	runtime.KeepAlive(env)
	defer windows.CloseHandle(pi.Process)
	defer windows.CloseHandle(pi.Thread)
	if err := windows.AssignProcessToJobObject(job, pi.Process); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		return 0, err
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = windows.TerminateProcess(pi.Process, 1)
		return 0, err
	}
	if _, err := windows.WaitForSingleObject(pi.Process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(pi.Process, &code); err != nil {
		return 0, err
	}
	return code, nil
}
