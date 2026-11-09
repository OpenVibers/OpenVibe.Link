package egress

import (
	"os/exec"
	"testing"
)

func TestCloudPluginStartup(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python not installed")
	}
	out, err := exec.Command(python, "../../plugins/computer/tests/test_link_egress.py").CombinedOutput()
	if err != nil {
		t.Fatalf("cloud plugin: %v\n%s", err, out)
	}
}
