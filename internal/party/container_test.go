package party

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type call struct {
	stdin string
	args  []string
}
type fakeRuntime struct {
	calls []call
	fail  string
}

func (f *fakeRuntime) Gateway(context.Context) (string, error) { return "127.0.0.1", nil }

func (f *fakeRuntime) Run(_ context.Context, stdin string, args ...string) error {
	f.calls = append(f.calls, call{stdin, args})
	if args[0] == f.fail {
		return errors.New("failed")
	}
	return nil
}

func turn() []ICEServer {
	return []ICEServer{{URLs: "turns:turn.openvibe.live:443?transport=tcp", Username: "temporary", Credential: "temporary"}}
}

func TestPlanUsesPinnedImageAndSeparateCredentials(t *testing.T) {
	p, err := NewPlan("party-0123abcd", 18180, turn())
	if err != nil {
		t.Fatal(err)
	}
	if p.MemberPassword == p.AdminPassword || p.MemberPassword == p.APIToken || p.AdminPassword == p.APIToken {
		t.Fatal("credentials must differ")
	}
	args := strings.Join(p.RunArgs(), " ")
	for _, expected := range []string{"--network bridge", "--security-opt=no-new-privileges", "--memory=3g", "--shm-size=2g", "127.0.0.1:18180:8080", Image} {
		if !strings.Contains(args, expected) {
			t.Errorf("run plan is missing %s", expected)
		}
	}
	for _, forbidden := range []string{p.MemberPassword, p.AdminPassword, p.APIToken, "--volume", "--mount", "--network host"} {
		if strings.Contains(args, forbidden) {
			t.Error("run arguments contain a credential or host access")
		}
	}
	env, err := p.EnvFile()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(env, "NEKO_WEBRTC_ICESERVERS_BACKEND=") || !strings.Contains(env, "NEKO_SESSION_COOKIE_ENABLED=true") {
		t.Fatal("missing TURN or cookie settings")
	}
}

func TestPlanRequiresTLSRelayAndValidID(t *testing.T) {
	for _, tc := range []struct {
		id  string
		ice []ICEServer
	}{
		{"oops", turn()},
		{"party-0123abcd", []ICEServer{{URLs: "turn:turn.openvibe.live:3478", Username: "x", Credential: "y"}}},
		{"party-0123abcd", []ICEServer{{URLs: "turns:turn.openvibe.live:443"}}},
	} {
		if _, err := NewPlan(tc.id, 8080, tc.ice); err == nil {
			t.Fatal("unsafe plan accepted")
		}
	}
}

func TestLifecycleCleansUp(t *testing.T) {
	p, err := NewPlan("party-0123abcd", 18180, turn())
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeRuntime{}
	stop, err := Start(context.Background(), f, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 4 || f.calls[0].args[0] != "pull" || f.calls[1].args[0] != "create" || f.calls[2].args[0] != "cp" || f.calls[3].args[0] != "start" {
		t.Fatal("pull, create, policy copy and start are required")
	}
	if !strings.Contains(f.calls[1].stdin, p.MemberPassword) {
		t.Fatal("env file was not passed to stdin")
	}
	if err := stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if f.calls[4].args[0] != "stop" || f.calls[5].args[0] != "rm" {
		t.Fatal("container did not stop")
	}
	f = &fakeRuntime{fail: "start"}
	if _, err := Start(context.Background(), f, p); err == nil {
		t.Fatal("run failure was lost")
	}
	if f.calls[len(f.calls)-1].args[0] != "rm" {
		t.Fatal("failed container was not removed")
	}
	f = &fakeRuntime{fail: "cp"}
	if _, err := Start(context.Background(), f, p); err == nil {
		t.Fatal("policy copy failure was lost")
	}
	for _, call := range f.calls {
		if call.args[0] == "start" {
			t.Fatal("container started without policy")
		}
	}
	if f.calls[len(f.calls)-1].args[0] != "rm" {
		t.Fatal("container with missing policy was not removed")
	}
}
