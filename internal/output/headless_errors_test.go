package output

import (
	"fmt"
	"testing"
)

func TestConfigReadOnlyError(t *testing.T) {
	e := NewError("config_read_only", nil)
	if e.Code != "config_read_only" || e.Message != "This configuration is read-only (headless mode)." ||
		e.NextAction != "Change the configuration at its source (for example the ConfigMap) and restart the runtime." ||
		ExitCode(e) != 2 || ExitCode(fmt.Errorf("wrapped: %w", e)) != 2 {
		t.Fatal(e, ExitCode(e))
	}
}

func TestHeadlessOnlyError(t *testing.T) {
	e := HeadlessOnlyError()
	if e.Code != "runtime_unsupported" || e.Message != "On Linux, MCParcel runs in headless mode only." ||
		e.NextAction != `Set runtime.mode to "headless" and runtime.stateRoot in config.json.` || ExitCode(e) != 2 {
		t.Fatal(e, ExitCode(e))
	}
	if NewError("runtime_unsupported", nil).Message == e.Message {
		t.Fatal("HeadlessOnlyError changed the registry entry")
	}
}
