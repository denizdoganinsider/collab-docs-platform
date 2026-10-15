package config

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

const (
	helperEnv   = "CONFIG_TEST_HELPER_PROCESS"
	intervalEnv = "LB_HEALTH_INTERVAL_SECONDS"
)

// TestLoadConfigHelperProcess is the child side of runLoadConfig: positiveInt
// calls os.Exit on a bad value, so it is observed from a separate process.
func TestLoadConfigHelperProcess(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process only")
	}
	cfg := LoadConfig()
	_, _ = os.Stdout.WriteString("interval=" + strconv.Itoa(cfg.LBHealthIntervalSeconds) + "\n")
	os.Exit(0)
}

// runLoadConfig runs LoadConfig in a child process with the interval variable
// set to value, or absent when set is false. It returns the exit code and the
// interval the child reported.
func runLoadConfig(t *testing.T, value string, set bool) (int, string) {
	t.Helper()
	env := []string{}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, intervalEnv+"=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, helperEnv+"=1", "JWT_SECRET=test-secret", "GATEWAY_SHARED_KEY=test-key")
	if set {
		env = append(env, intervalEnv+"="+value)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestLoadConfigHelperProcess$")
	cmd.Env = env
	out, err := cmd.Output()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("running helper process: %v", err)
		}
		code = exitErr.ExitCode()
	}
	interval := ""
	for line := range strings.SplitSeq(string(out), "\n") {
		if v, ok := strings.CutPrefix(line, "interval="); ok {
			interval = v
		}
	}
	return code, interval
}

func TestLoadConfigRejectsUnusableHealthInterval(t *testing.T) {
	for _, value := range []string{"0", "-1", "5s", "abc"} {
		code, _ := runLoadConfig(t, value, true)
		if code == 0 {
			t.Errorf("%s=%q: exit code 0, want a non-zero startup failure", intervalEnv, value)
		}
	}
}

func TestLoadConfigAcceptsPositiveHealthInterval(t *testing.T) {
	code, interval := runLoadConfig(t, "2", true)
	if code != 0 {
		t.Fatalf("%s=2: exit code %d, want 0", intervalEnv, code)
	}
	if interval != "2" {
		t.Errorf("%s=2: LBHealthIntervalSeconds = %q, want 2", intervalEnv, interval)
	}
}

func TestLoadConfigWiresHealthInterval(t *testing.T) {
	t.Setenv("JWT_SECRET", "test-secret")
	t.Setenv("GATEWAY_SHARED_KEY", "test-key")
	t.Setenv(intervalEnv, "2")

	if got := LoadConfig().LBHealthIntervalSeconds; got != 2 {
		t.Errorf("LBHealthIntervalSeconds = %d, want 2", got)
	}
}

func TestLoadConfigHealthIntervalFallsBackWhenUnsetOrEmpty(t *testing.T) {
	cases := []struct {
		name  string
		value string
		set   bool
	}{
		{name: "unset", set: false},
		{name: "empty", value: "", set: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, interval := runLoadConfig(t, tc.value, tc.set)
			if code != 0 {
				t.Fatalf("exit code %d, want 0", code)
			}
			if interval != "5" {
				t.Errorf("LBHealthIntervalSeconds = %q, want the default 5", interval)
			}
		})
	}
}
