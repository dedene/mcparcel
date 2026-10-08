package runtime

import (
	"slices"
	"testing"

	"github.com/dedene/mcparcel/internal/testutil"
)

// With the real bus check, TCP, abstract and /tmp addresses never reach the
// desktop daemon.
func TestDaemonEnvironmentDropsUnsafeBus(t *testing.T) {
	p, _ := testutil.IsolatedPaths(t)
	for _, value := range []string{"tcp:host=x,port=1", "unix:abstract=x", "unix:path=/tmp/bus", "autolaunch:", "unix:path=/run/user/1/bus;tcp:host=x"} {
		t.Setenv(busEnv, value)
		if env := DaemonEnvironment(p); slices.Contains(env, busEnv+"="+value) {
			t.Fatal(value, env)
		}
	}
}
