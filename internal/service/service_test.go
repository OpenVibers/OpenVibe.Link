package service

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestProgramStartStop(t *testing.T) {
	stopped := make(chan struct{})
	p := &program{run: func(ctx context.Context) error {
		<-ctx.Done()
		time.Sleep(10 * time.Millisecond) // stopping actuators
		close(stopped)
		return nil
	}}
	if err := p.Start(nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Stop(nil); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	default:
		t.Fatal("Stop returned before the program finished")
	}
}

func TestConfig(t *testing.T) {
	c := config(Options{Arguments: []string{"run"}})
	if c.Name != "openvibe-link" || c.Arguments[0] != "run" || c.Option["Restart"] != "always" {
		t.Fatalf("%+v", c)
	}
	if u, _ := c.Option["SystemdScript"].(string); !strings.Contains(u, "\n[Service]\n") || !strings.Contains(u, "\nDelegate=yes\n") {
		t.Fatalf("the systemd unit must delegate the Link's cgroup: %q", u)
	}
	// A revoked node credential exits with ExitPairAgain: systemd must not restart it into the same refusal.
	if u, _ := c.Option["SystemdScript"].(string); !strings.Contains(u, fmt.Sprintf("\nRestartPreventExitStatus=%d\n", ExitPairAgain)) {
		t.Fatalf("the systemd unit must not restart a Link that has to pair again: %q", u)
	}
}

// A Link that runs as a person must not gain privileges through a setuid child, so its unit refuses
// new privileges. The privileged system unit does not: the shared desk host runs as root on purpose.
func TestUserServicePreventsPrivilegeGain(t *testing.T) {
	unit := config(Options{UserName: "person"}).Option["SystemdScript"].(string)
	// The directive sits inside the {{if UserName}} guard: only the per-user unit gets it.
	if !strings.Contains(unit, "{{if UserName}}User={{UserName}}\nNoNewPrivileges=yes\n{{end}}") {
		t.Fatalf("the per-user unit must set NoNewPrivileges=yes: %q", unit)
	}
}
