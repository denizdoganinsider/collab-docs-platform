package config

import (
	"errors"
	"os"
	"os/exec"
	"slices"
	"testing"
)

const exitHelperEnv = "CONFIG_TEST_LOAD_AND_EXIT"

// TestLoadConfigSubprocess is the child side of TestPositiveIntRejects: it
// only runs when re-executed with exitHelperEnv set.
func TestLoadConfigSubprocess(t *testing.T) {
	if os.Getenv(exitHelperEnv) != "1" {
		t.Skip("subprocess helper")
	}
	LoadConfig()
}

func TestPositiveIntRejects(t *testing.T) {
	for _, tc := range []struct {
		name     string
		env      string
		wantExit bool
	}{
		{"duration string", "SNAPSHOT_EVERY_SECONDS=30s", true},
		{"zero ring", "OP_RING_SIZE=0", true},
		{"valid value", "OP_RING_SIZE=5", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestLoadConfigSubprocess$")
			cmd.Env = append(os.Environ(), exitHelperEnv+"=1", "GATEWAY_SHARED_KEY=k", tc.env)
			err := cmd.Run()

			var exitErr *exec.ExitError
			exited := errors.As(err, &exitErr) && exitErr.ExitCode() != 0
			if err != nil && !exited {
				t.Fatalf("subprocess: %v", err)
			}
			if exited != tc.wantExit {
				t.Fatalf("%s: non-zero exit = %v, want %v", tc.env, exited, tc.wantExit)
			}
		})
	}
}

func TestPositiveIntParses(t *testing.T) {
	t.Setenv("GATEWAY_SHARED_KEY", "k")
	t.Setenv("OP_RING_SIZE", "7")

	if got := LoadConfig().OpRingSize; got != 7 {
		t.Fatalf("OpRingSize = %d, want 7", got)
	}
}

func TestGetEnvListTrimsAndDropsEmpty(t *testing.T) {
	t.Setenv("GATEWAY_SHARED_KEY", "k")
	t.Setenv("WS_ALLOWED_ORIGINS", " https://a.test , https://b.test,,")

	want := []string{"https://a.test", "https://b.test"}
	if got := LoadConfig().WSAllowedOrigins; !slices.Equal(got, want) {
		t.Fatalf("WSAllowedOrigins = %q, want %q", got, want)
	}
}

func TestGetEnvListBlankFallsBack(t *testing.T) {
	t.Setenv("GATEWAY_SHARED_KEY", "k")
	t.Setenv("WS_ALLOWED_ORIGINS", " , ")

	want := []string{"http://localhost:9000"}
	if got := LoadConfig().WSAllowedOrigins; !slices.Equal(got, want) {
		t.Fatalf("WSAllowedOrigins = %q, want %q", got, want)
	}
}
