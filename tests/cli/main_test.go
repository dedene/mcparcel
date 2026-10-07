package cli_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dedene/mcparcel/internal/config"
	runtimeclient "github.com/dedene/mcparcel/internal/runtime"
	"github.com/dedene/mcparcel/internal/testutil"
)

var binaryA, binaryB, fixtureBinary, buildRoot string

// test-cli: make ci runs this untagged package through go test -race ./...;
// mcparceltest applies only to the CLI binaries built by TestMain.
func TestMain(m *testing.M) {
	if os.Getenv("MCPARCEL_FIXTURE_STDIO") == "1" {
		os.Exit(runFixture())
	}
	repo := repoRoot()
	parent := buildParent()
	if err := os.MkdirAll(parent, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	root, err := os.MkdirTemp(parent, "cli-build-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	buildRoot = root
	fixtureBinary, _ = os.Executable()
	binaryA, binaryB = filepath.Join(root, "mcparcel-a"), filepath.Join(root, "mcparcel-b")
	for i, binary := range []string{binaryA, binaryB} {
		version := []string{"stage2-test-a", "stage2-test-b"}[i]
		cmd := exec.Command("go", "build", "-race", "-tags=mcparceltest", "-ldflags", "-X github.com/dedene/mcparcel/internal/cmd.version="+version, "-o", binary, "./cmd/mcparcel")
		cmd.Dir = repo
		// The pinned toolchain and module cache require the complete inherited Go environment.
		cmd.Env = replaceEnv(os.Environ(), "CGO_ENABLED", "1")
		if output, err := cmd.CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build: %v\n%s", err, output)
			_ = os.RemoveAll(root)
			os.Exit(1)
		}
	}
	sentinel := filepath.Join(root, "sentinel")
	for _, key := range []string{"HOME", "XDG_CONFIG_HOME"} {
		dir := filepath.Join(sentinel, key)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.Setenv(key, dir); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	code := m.Run()
	_ = filepath.WalkDir(sentinel, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			code = 1
			return err
		}
		if !d.IsDir() {
			fmt.Fprintln(os.Stderr, "test host sentinel modified:", path)
			code = 1
		}
		return nil
	})
	if err := os.RemoveAll(root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

func repoRoot() string {
	_, source, _, _ := goruntime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
}

// buildParent holds the built binaries. On macOS it is the gitignored .scratch,
// not /private/tmp; .scratch itself is shared and kept. The Linux container
// mounts the source read-only, so there they go under the temp root.
func buildParent() string {
	if goruntime.GOOS == "darwin" {
		return filepath.Join(repoRoot(), ".scratch")
	}
	return testutil.TempRoot()
}

type rig struct {
	t        *testing.T
	root     string
	bin      string
	paths    config.Paths
	env      []string
	personal config.Personal
	pids     map[int]bool
	closers  []func()
}
type result struct {
	code           int
	stdout, stderr string
	envelope       struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
}
type process struct {
	cmd      *exec.Cmd
	out, err bytes.Buffer
	done     chan struct{}
	waitErr  error
}

func newRig(t *testing.T) *rig {
	t.Helper()
	// Runtime dirs and sockets stay short under the temp root (104-byte socket path limit).
	root, err := os.MkdirTemp(testutil.TempRoot(), "cli-test-")
	if err != nil {
		t.Fatal(err)
	}
	bin, err := os.MkdirTemp(buildRoot, "rig-")
	if err != nil {
		t.Fatal(err)
	}
	for i, binary := range []string{binaryA, binaryB} {
		if e := os.Link(binary, filepath.Join(bin, []string{"cli-a", "cli-b"}[i])); e != nil {
			t.Fatal(e)
		}
	}
	r := &rig{t: t, root: root, bin: bin, pids: map[int]bool{}, personal: config.Personal{SchemaVersion: 1, Connections: map[string]config.Connection{}, CredentialProfiles: map[string]config.ProfileRequirement{}}}
	r.env = []string{"HOME=" + root + "/home", "TMPDIR=" + root + "/tmp", "SHELL=" + root + "/shell", "XDG_CONFIG_HOME=" + root + "/config", "XDG_DATA_HOME=" + root + "/data", "XDG_CACHE_HOME=" + root + "/cache", "XDG_STATE_HOME=" + root + "/state", "MCPARCEL_RUNTIME_DIR=" + root + "/run", "PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "LC_ALL=C", "MCPARCEL_SENTINEL_HOME=" + root + "/sentinel", testutil.RaceExitEnv}
	r.paths, err = config.ResolvePaths(func(k string) string { return envValue(r.env, k) }, root+"/home", testutil.TempRoot(), os.Getuid())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{r.paths.Home, root + "/tmp", root + "/sentinel"} {
		if err = os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	r.write(root+"/shell", "#!/bin/sh\nexec /bin/sh -c \"$3\"\n", 0o700)
	t.Cleanup(func() {
		r.pids = make(map[int]bool)
		// Query only this test's private socket, including after version replacement.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		client := runtimeclient.Client{Paths: r.paths, Version: "stage2-test-a", Executable: binaryA}
		for _, version := range []string{"stage2-test-a", "stage2-test-b"} {
			client.Version = version
			if status, e := client.Status(ctx); e == nil && status.Running {
				r.pids[status.PID] = true
			}
		}
		cancel()
		for _, pid := range r.daemonPIDs() {
			r.pids[pid] = true
		}
		children := []int{}
		for pid := range r.pids {
			children = append(children, ownedChildren(t, pid)...)
			_ = syscall.Kill(pid, syscall.SIGTERM)
		}
		deadline := time.Now().Add(6 * time.Second)
		for pid := range r.pids {
			for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
				<-time.After(20 * time.Millisecond)
			}
			if syscall.Kill(pid, 0) == nil {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		for _, pid := range children {
			for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
				<-time.After(20 * time.Millisecond)
			}
			if syscall.Kill(pid, 0) == nil {
				_ = syscall.Kill(-pid, syscall.SIGKILL)
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		allPIDs := append([]int(nil), children...)
		for pid := range r.pids {
			allPIDs = append(allPIDs, pid)
		}
		for _, pid := range allPIDs {
			deadline := time.Now().Add(2 * time.Second)
			for syscall.Kill(pid, 0) == nil && time.Now().Before(deadline) {
				<-time.After(10 * time.Millisecond)
			}
			if syscall.Kill(pid, 0) == nil {
				t.Errorf("owned process %d remained after shutdown", pid)
			}
		}
		for _, closeResource := range r.closers {
			closeResource()
		}
		entries, e := os.ReadDir(root + "/sentinel")
		if e != nil || len(entries) != 0 {
			t.Errorf("sentinel changed: %v %v", entries, e)
		}
		if e = os.RemoveAll(root); e != nil {
			t.Error(e)
		}
		if e = os.RemoveAll(r.bin); e != nil {
			t.Error(e)
		}
	})
	r.stdio("fixture", "")
	return r
}

func envValue(env []string, key string) string {
	for _, v := range env {
		if strings.HasPrefix(v, key+"=") {
			return strings.TrimPrefix(v, key+"=")
		}
	}
	return ""
}

func replaceEnv(env []string, key, value string) []string {
	out := make([]string, 0, len(env)+1)
	for _, v := range env {
		if !strings.HasPrefix(v, key+"=") {
			out = append(out, v)
		}
	}
	return append(out, key+"="+value)
}

func (r *rig) write(path, body string, mode os.FileMode) {
	r.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		r.t.Fatal(err)
	}
}
func literal(s string) config.Value { return config.Value{Literal: &s} }
func (r *rig) stdio(id, ref string) {
	c := config.Connection{Transport: config.Transport{Stdio: &config.Stdio{Command: config.Literal(fixtureBinary), Env: map[string]config.Value{"MCPARCEL_FIXTURE_STDIO": literal("1"), "MCPARCEL_FIXTURE_ROOT": literal(r.root), "MCPARCEL_FIXTURE_STARTED_FILE": literal(r.root + "/started"), "MCPARCEL_FIXTURE_RELEASE_FILE": literal(r.root + "/release"), "MCPARCEL_FIXTURE_WRITE_FILE": literal(r.root + "/writes")}}}}
	if ref != "" {
		c.CredentialProfile = "shared"
		c.Transport.Stdio.Env["API_KEY"] = config.Value{Secret: &config.SecretRef{Secret: ref}}
		r.personal.CredentialProfiles["shared"] = config.ProfileRequirement{}
	}
	r.personal.Connections[id] = c
	r.save()
}

func (r *rig) save() {
	r.t.Helper()
	b, e := json.Marshal(r.personal)
	if e != nil {
		r.t.Fatal(e)
	}
	r.write(r.paths.PersonalFile, string(b), 0o600)
	local := config.Local{SchemaVersion: 1, CredentialProfiles: map[string]config.Profile{"shared": {Mode: "desktop-service-account", Account: "Fixture account", BootstrapRef: "op://Private/fixture/token", SessionDuration: "24h"}}}
	b, e = json.Marshal(local)
	if e != nil {
		r.t.Fatal(e)
	}
	r.write(r.paths.ConfigFile, string(b), 0o600)
}

func (r *rig) start(binary, input string, args ...string) *process {
	return r.startReader(binary, strings.NewReader(input), args...)
}

func (r *rig) startReader(binary string, input io.Reader, args ...string) *process {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	r.t.Cleanup(cancel)
	switch binary {
	case binaryA:
		binary = r.bin + "/cli-a"
	case binaryB:
		binary = r.bin + "/cli-b"
	}
	p := &process{cmd: exec.CommandContext(ctx, binary, args...), done: make(chan struct{})}
	p.cmd.Env = append([]string(nil), r.env...)
	p.cmd.Dir = r.paths.Home
	p.cmd.Stdin = input
	p.cmd.Stdout = &p.out
	p.cmd.Stderr = &p.err
	p.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if e := p.cmd.Start(); e != nil {
		r.t.Fatal(e)
	}
	go func() { p.waitErr = p.cmd.Wait(); close(p.done) }()
	r.t.Cleanup(func() {
		select {
		case <-p.done:
		default:
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			<-p.done
		}
	})
	return p
}

func (r *rig) finish(p *process) result {
	r.t.Helper()
	<-p.done
	err := p.waitErr
	code := 0
	if err != nil {
		if p.cmd.ProcessState != nil {
			code = p.cmd.ProcessState.ExitCode()
			if code < 0 {
				r.t.Logf("CLI terminated by signal: %v", err)
			}
		} else {
			r.t.Fatal(err)
		}
	}
	return result{code: code, stdout: p.out.String(), stderr: p.err.String()}
}
func (r *rig) run(args ...string) result { return r.finish(r.start(binaryA, "", args...)) }
func (r *rig) check(v result, exit int, code string) result {
	r.t.Helper()
	if v.code != exit {
		r.t.Fatalf("exit %d want %d: stdout=%s stderr=%s", v.code, exit, v.stdout, v.stderr)
	}
	if !strings.HasSuffix(v.stdout, "\n") || strings.HasSuffix(v.stdout, "\n\n") {
		r.t.Fatalf("stdout must end in exactly one newline: %q", v.stdout)
	}
	decoder := json.NewDecoder(strings.NewReader(v.stdout))
	if e := decoder.Decode(&v.envelope); e != nil {
		r.t.Fatalf("one JSON envelope required: %q: %v", v.stdout, e)
	}
	if decoder.InputOffset() != int64(len(v.stdout)-1) {
		r.t.Fatalf("stdout trailing bytes must be exactly newline: %q", v.stdout)
	}
	if v.envelope.OK != (code == "") || v.envelope.Error.Code != code {
		r.t.Fatalf("unexpected envelope: %s", v.stdout)
	}
	if strings.Contains(v.stdout, "FIXTURE-CHILD-STDERR") {
		r.t.Fatal("child stderr leaked to JSON stdout")
	}
	if v.stderr != "" {
		r.t.Fatalf("JSON diagnostic leaked to stderr: %q", v.stderr)
	}
	return v
}

func (r *rig) call(target string, args ...string) result {
	return r.check(r.run(append([]string{"call", target, "--json"}, args...)...), 0, "")
}

func (r *rig) status() runtimeclient.Status {
	r.t.Helper()
	v := r.check(r.run("runtime", "status", "--json"), 0, "")
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(v.envelope.Data, &fields); err != nil {
		r.t.Fatal(err)
	}
	keys := []string{"running", "pid", "protocolVersion", "binaryVersion", "compatible", "socket", "log", "capturedPath", "envFallback", "activeCalls", "stayAlive", "startedAt"}
	if _, ok := fields["credentialSessions"]; ok {
		keys = append(keys, "credentialSessions")
	}
	if len(fields) != len(keys) {
		r.t.Fatalf("status fields: %s", v.envelope.Data)
	}
	for _, key := range keys {
		if _, ok := fields[key]; !ok {
			r.t.Fatalf("status missing %s", key)
		}
	}
	var s runtimeclient.Status
	if e := json.Unmarshal(v.envelope.Data, &s); e != nil {
		r.t.Fatal(e)
	}
	if s.Running {
		r.pids[s.PID] = true
	}
	return s
}

func (r *rig) waitFile(path, contains string, processes ...*process) {
	r.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range processes {
			select {
			case <-p.done:
				v := r.finish(p)
				r.t.Fatalf("process ended before barrier: wait=%v stdout=%s stderr=%s", p.waitErr, v.stdout, v.stderr)
			default:
			}
		}
		b, e := os.ReadFile(path)
		if e == nil && strings.Contains(string(b), contains) {
			return
		}
		<-time.After(10 * time.Millisecond)
	}
	if data, e := os.ReadFile(r.paths.LogFile); e == nil {
		r.t.Logf("daemon log at barrier failure: %s", data)
	}
	r.t.Fatalf("barrier %s did not reach %q", path, contains)
}

func (r *rig) countEvents(name string) int {
	b, e := os.ReadFile(r.paths.StateDir + "/fixture-auth-events")
	if e != nil && !os.IsNotExist(e) {
		r.t.Fatal(e)
	}
	n := 0
	for _, line := range strings.Split(string(b), "\n") {
		if line == name {
			n++
		}
	}
	return n
}

func structured(t *testing.T, v result) map[string]any {
	t.Helper()
	var d struct {
		Result struct {
			Structured map[string]any `json:"structuredContent"`
		} `json:"result"`
	}
	if e := json.Unmarshal(v.envelope.Data, &d); e != nil {
		t.Fatal(e)
	}
	return d.Result.Structured
}

func (r *rig) daemonPIDs() []int {
	out, e := exec.Command("/bin/ps", "-axo", "pid=,command=").Output()
	if e != nil {
		r.t.Error("cannot observe owned daemon processes:", e)
		return nil
	}
	var pids []int
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && (fields[1] == r.bin+"/cli-a" || fields[1] == r.bin+"/cli-b") && fields[2] == "daemon" {
			var pid int
			if _, err := fmt.Sscanf(fields[0], "%d", &pid); err == nil {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

func (r *rig) waitActive(count int) {
	r.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	client := runtimeclient.Client{Paths: r.paths, Version: "stage2-test-a", Executable: binaryA}
	for {
		status, err := client.Status(ctx)
		if err == nil && status.ActiveCalls >= count {
			r.pids[status.PID] = true
			return
		}
		select {
		case <-ctx.Done():
			r.t.Fatalf("daemon never admitted %d concurrent callers", count)
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestBlackBoxBinariesInScratch(t *testing.T) {
	parent := buildParent()
	under := func(path string) bool {
		rel, err := filepath.Rel(parent, path)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, "../")
	}
	for _, binary := range []string{binaryA, binaryB} {
		if !under(binary) {
			t.Fatalf("binary %s outside %s", binary, parent)
		}
	}
	r := newRig(t)
	if !under(r.bin) || !strings.HasPrefix(r.root, testutil.TempRoot()+"/cli-test-") {
		t.Fatalf("rig bin=%s root=%s", r.bin, r.root)
	}
	if _, err := os.Lstat(r.root + "/cli-a"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("binary link under runtime root: %v", err)
	}
	r.call("fixture.counter")
	if len(r.daemonPIDs()) == 0 {
		t.Fatal("daemonPIDs did not find the daemon started from the scratch link")
	}
}

// Positive control for tests/packaging: the tagged binary carries the fixture
// stand-ins that the untagged release binary must not.
func TestFixtureBinaryHasStandIns(t *testing.T) {
	b, err := os.ReadFile(binaryA)
	if err != nil {
		t.Fatal(err)
	}
	for _, marker := range []string{"fixture-terminal", "fixture-dialog-answer", "fixture-keyring"} {
		if !bytes.Contains(b, []byte(marker)) {
			t.Fatal("tagged binary lacks", marker)
		}
	}
}
