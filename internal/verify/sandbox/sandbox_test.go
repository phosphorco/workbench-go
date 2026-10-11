package sandbox_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/verify/sandbox"
)

const image = "alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8"

func invocation(t *testing.T, script string) sandbox.Invocation {
	t.Helper()
	root := t.TempDir()
	dirs := []string{"work", "verifier", "out"}
	for _, name := range dirs {
		if err := os.Mkdir(filepath.Join(root, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	return sandbox.Invocation{Image: image, Command: []string{"/bin/sh", "-c", script}, UnitID: "unit<&>", Role: "run", WorkDir: filepath.Join(root, "work"), VerifierDir: filepath.Join(root, "verifier"), OutputDir: filepath.Join(root, "out"), Network: "none", Timeout: 15 * time.Second}
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, in sandbox.Invocation) sandbox.Outcome {
	t.Helper()
	out, err := sandbox.Execute(context.Background(), in)
	if err != nil {
		t.Fatalf("Execute: %v; stderr=%s", err, out.Stderr)
	}
	if out.ExitCode != 0 || out.TimedOut {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	return out
}

func TestUndeclaredAbsent(t *testing.T) {
	in := invocation(t, `set -eu; test "$(cat /work/declared)" = selected; test ! -e /work/undeclared; test ! -e /work/.git; test ! -e /work/checkout; printf verified > /out/report.json`)
	checkout := t.TempDir()
	write(t, filepath.Join(checkout, "declared"), "selected")
	write(t, filepath.Join(checkout, "undeclared"), "secret")
	write(t, filepath.Join(in.WorkDir, "declared"), "selected")
	out := run(t, in)
	if len(out.Outputs) != 1 || string(out.Outputs[0].Bytes) != "verified" {
		t.Fatalf("outputs: %+v", out.Outputs)
	}
	// An unsound closure must produce a tool failure when the program reads it.
	in.Command = []string{"cat", "/work/undeclared"}
	out, err := sandbox.Execute(context.Background(), in)
	var failure *sandbox.ToolFailure
	if !errors.As(err, &failure) || out.ExitCode == 0 {
		t.Fatalf("undeclared read: %+v, %v", out, err)
	}
}

func TestSymlinkReportRefused(t *testing.T) {
	hostReport := filepath.Join(t.TempDir(), "host-report.json")
	write(t, hostReport, "host bytes must not be collected")
	for _, target := range []string{hostReport, "/work/host-report", "real.json"} {
		t.Run(target, func(t *testing.T) {
			in := invocation(t, `printf ordinary > /out/real.json; ln -s "$TARGET" /out/report.json`)
			// The command embeds a fixed fixture, never an ambient environment grant.
			in.Command = []string{"sh", "-c", `printf ordinary > /out/real.json; ln -s "$1" /out/report.json`, "sh", target}
			write(t, filepath.Join(in.WorkDir, "host-report"), "host bytes must not be collected")
			out := run(t, in)
			if len(out.Outputs) != 2 || out.Outputs[1].Mode != "120000" || string(out.Outputs[1].Bytes) != target {
				t.Fatalf("artifact link was not recorded: %+v", out.Outputs)
			}
			report, err := sandbox.ReadRegularOutput(in.OutputDir, "report.json")
			if err == nil || report.Bytes != nil {
				t.Fatalf("symlink Report accepted: %+v, %v", report, err)
			}
		})
	}
}

func TestEnvCleared(t *testing.T) {
	t.Setenv("SANDBOX_HOST_SECRET", "must not be visible")
	t.Setenv("VERIFY_UNIT", "ambient unit")
	t.Setenv("HOME", "/host/home")
	in := invocation(t, "")
	in.Command = []string{"/usr/bin/env"}
	out := run(t, in)
	got := strings.Split(strings.TrimSpace(string(out.Stdout)), "\n")
	sort.Strings(got)
	want := []string{"HOME=/tmp", "PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", "VERIFY_ROLE=run", "VERIFY_UNIT=unit<&>"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("environment: %q", got)
	}
}

func TestNetworkNoneOffline(t *testing.T) {
	service, err := exec.Command("docker", "run", "--detach", "--network", "bridge", "--entrypoint", "sh", image, "-c", `cat > /tmp/serve <<'SH'
#!/bin/sh
read -r request
printf 'HTTP/1.0 200 OK\r\nContent-Length: 10\r\n\r\ndependency'
SH
chmod +x /tmp/serve
exec nc -lk -p 8080 -e /tmp/serve`).Output()
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(service))
	t.Cleanup(func() {
		if data, err := exec.Command("docker", "rm", "--force", id).CombinedOutput(); err != nil {
			t.Errorf("service cleanup: %s %v", data, err)
		}
	})
	address, err := exec.Command("docker", "inspect", "--format", "{{.NetworkSettings.Networks.bridge.IPAddress}}", id).Output()
	if err != nil {
		t.Fatal(err)
	}
	url := fmt.Sprintf("http://%s:8080/", strings.TrimSpace(string(address)))
	in := invocation(t, "")
	in.Command = []string{"wget", "-T", "2", "-O", "/out/dependency", url}
	out, err := sandbox.Execute(context.Background(), in)
	var failure *sandbox.ToolFailure
	if !errors.As(err, &failure) || out.ExitCode == 0 {
		t.Fatalf("offline download succeeded: %+v, %v", out, err)
	}
	scratch := t.TempDir()
	in.ScratchDir = scratch
	in.Network = "unrestricted"
	in.Command = []string{"sh", "-c", `set -eu; wget -T 2 -O /scratch/cache "$1"; cp /scratch/cache /out/dependency`, "sh", url}
	out = run(t, in)
	if len(out.Outputs) != 1 || string(out.Outputs[0].Bytes) != "dependency" {
		t.Fatalf("bridge download: %+v", out.Outputs)
	}
	in.Network = "none"
	in.Command = []string{"sh", "-c", `set -eu; test "$(ip route | wc -l)" = 0; cp /scratch/cache /out/dependency`}
	out = run(t, in)
	if len(out.Outputs) != 1 || string(out.Outputs[0].Bytes) != "dependency" {
		t.Fatalf("offline cache: %+v", out.Outputs)
	}
}

func TestReadOnlyMountsAndExecutableTmp(t *testing.T) {
	in := invocation(t, `set -eu
 test "$(cat /work/input)" = input
 test "$(cat /verifier/program)" = program
 test "$(cat /upstream/build/artifact)" = artifact
 if printf changed > /work/input; then exit 10; fi
 if printf changed > /verifier/program; then exit 11; fi
 if printf changed > /upstream/build/artifact; then exit 12; fi
 if touch /root-write; then exit 13; fi
 test ! -e /scratch
 printf '#!/bin/sh\nprintf tmp-exec' > /tmp/program
 chmod +x /tmp/program
 /tmp/program > /out/result
 mkdir /out/nested
 printf '#!/bin/sh\n' > /out/nested/executable
 chmod +x /out/nested/executable
 printf stdout
 printf stderr >&2`)
	upstream := t.TempDir()
	in.Upstream = []sandbox.Upstream{{Name: "build", HostDir: upstream}}
	write(t, filepath.Join(in.WorkDir, "input"), "input")
	write(t, filepath.Join(in.VerifierDir, "program"), "program")
	write(t, filepath.Join(upstream, "artifact"), "artifact")
	out := run(t, in)
	if string(out.Stdout) != "stdout" || !strings.HasSuffix(out.Stderr, "stderr") {
		t.Fatalf("streams: %+v", out)
	}
	if len(out.Outputs) != 2 || out.Outputs[0].Path != "nested/executable" || out.Outputs[0].Mode != "100755" || out.Outputs[1].Mode != "100644" || string(out.Outputs[1].Bytes) != "tmp-exec" {
		t.Fatalf("outputs: %+v", out.Outputs)
	}
	for path, want := range map[string]string{filepath.Join(in.WorkDir, "input"): "input", filepath.Join(in.VerifierDir, "program"): "program", filepath.Join(upstream, "artifact"): "artifact"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("input changed: %s %q %v", path, got, err)
		}
	}
}

func TestTimeoutKillsAndRemoves(t *testing.T) {
	in := invocation(t, `cat /etc/hostname; printf started > /out/started; sleep 60 & wait`)
	in.Timeout = 2 * time.Second
	start := time.Now()
	out, err := sandbox.Execute(context.Background(), in)
	var failure *sandbox.ToolFailure
	if !errors.As(err, &failure) || !out.TimedOut || !errors.Is(err, context.DeadlineExceeded) || out.Outputs != nil {
		t.Fatalf("timeout was not a tool failure: %+v %v", out, err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("timeout did not stop container promptly")
	}
	if _, err := os.Stat(filepath.Join(in.OutputDir, "started")); err != nil {
		t.Fatalf("container never started: %v", err)
	}
	id := strings.TrimSpace(string(out.Stdout))
	if id == "" {
		t.Fatal("container did not report its identity")
	}
	if data, err := exec.Command("docker", "inspect", id).CombinedOutput(); err == nil {
		t.Fatalf("container leaked: %s", data)
	}

}

func TestNonzeroIsToolFailure(t *testing.T) {
	in := invocation(t, `printf evidence; printf problem >&2; printf untrusted > /out/report.json; exit 23`)
	out, err := sandbox.Execute(context.Background(), in)
	var failure *sandbox.ToolFailure
	if !errors.As(err, &failure) || out.ExitCode != 23 || string(out.Stdout) != "evidence" || out.Stderr != "problem" || out.Outputs != nil || out.TimedOut {
		t.Fatalf("nonzero: %+v %v", out, err)
	}
}

func TestCollectRefusesSpecialFiles(t *testing.T) {
	for _, kind := range []string{"fifo", "socket"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "report.json")
			switch kind {
			case "fifo":
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "socket":
				listener, err := net.Listen("unix", path)
				if err != nil {
					t.Fatal(err)
				}
				defer listener.Close()
			}
			outputs, err := sandbox.CollectOutputs(dir)
			if err == nil || outputs != nil {
				t.Fatalf("special file accepted: %+v %v", outputs, err)
			}
		})
	}
}

func TestInvalidSpecRefused(t *testing.T) {
	base := invocation(t, "true")
	cases := []sandbox.Invocation{}
	bad := base
	bad.Image = "alpine:latest"
	cases = append(cases, bad)
	bad = base
	bad.Network = "host"
	cases = append(cases, bad)
	bad = base
	bad.Role = "other"
	cases = append(cases, bad)
	bad = base
	bad.Timeout = 0
	cases = append(cases, bad)
	bad = base
	bad.Command = nil
	cases = append(cases, bad)
	bad = base
	bad.Upstream = []sandbox.Upstream{{Name: "../escape", HostDir: base.WorkDir}}
	cases = append(cases, bad)
	bad = base
	bad.Upstream = []sandbox.Upstream{{Name: "a", HostDir: base.WorkDir}, {Name: "a", HostDir: base.WorkDir}}
	cases = append(cases, bad)
	bad = base
	bad.WorkDir = ""
	cases = append(cases, bad)
	bad = base
	bad.Role = "select"
	cases = append(cases, bad)
	for i, in := range cases {
		if _, err := sandbox.Execute(context.Background(), in); err == nil {
			t.Fatalf("invalid case %d accepted", i)
		}
	}
}

func TestReferenceStdinParity(t *testing.T) {
	data, err := os.ReadFile("testdata/stdin.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		UnitID string `json:"unitId"`
		Role   string `json:"role"`
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		in := invocation(t, "")
		in.UnitID = fixture.UnitID
		in.Role = fixture.Role
		in.Command = []string{"cat"}
		out := run(t, in)
		if !bytes.Equal(out.Stdout, []byte(fixture.Stdout)) {
			t.Fatalf("stdin parity: %q != %q", out.Stdout, fixture.Stdout)
		}
	}
}

func TestCancellationKillsAndRemoves(t *testing.T) {
	in := invocation(t, `cat /etc/hostname; printf started > /out/started; sleep 60 & wait`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := os.Stat(filepath.Join(in.OutputDir, "started")); err == nil {
					cancel()
					return
				}
			}
		}
	}()
	out, err := sandbox.Execute(ctx, in)
	cancel()
	<-done
	var failure *sandbox.ToolFailure
	if !errors.As(err, &failure) || !errors.Is(err, context.Canceled) || out.TimedOut || out.Outputs != nil {
		t.Fatalf("cancellation: %+v %v", out, err)
	}
	id := strings.TrimSpace(string(out.Stdout))
	if id == "" {
		t.Fatal("container never started")
	}
	if data, err := exec.Command("docker", "inspect", id).CombinedOutput(); err == nil {
		t.Fatalf("container leaked: %s", data)
	}
}

func TestCollectSortedPathsAndRootSymlinkRefused(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "a"), 0755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "a", "b"), "nested")
	write(t, filepath.Join(dir, "a.txt"), "top")
	out, err := sandbox.CollectOutputs(dir)
	if err != nil || len(out) != 2 || out[0].Path != "a.txt" || out[1].Path != "a/b" {
		t.Fatalf("ordering: %+v %v", out, err)
	}
	link := filepath.Join(t.TempDir(), "out")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	if _, err := sandbox.CollectOutputs(link); err == nil {
		t.Fatal("symlink root accepted")
	}
}

func TestCommaInMountPath(t *testing.T) {
	in := invocation(t, `cat /work/input > /out/result`)
	work := filepath.Join(t.TempDir(), "with,comma")
	if err := os.Mkdir(work, 0755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(work, "input"), "comma mount")
	in.WorkDir = work
	out := run(t, in)
	if len(out.Outputs) != 1 || string(out.Outputs[0].Bytes) != "comma mount" {
		t.Fatalf("mount: %+v", out)
	}
}

func TestArtifactSymlinksRecorded(t *testing.T) {
	hostFile := filepath.Join(t.TempDir(), "host-secret")
	write(t, hostFile, "must not be read")
	in := invocation(t, "")
	in.Command = []string{"sh", "-c", `set -eu
 mkdir -p /out/node_modules/.bin /out/node_modules/package
 printf executable > /out/node_modules/tool
 chmod +x /out/node_modules/tool
 printf workspace > /out/node_modules/package/source
 ln -s ../tool /out/node_modules/.bin/tool
 ln -s package /out/node_modules/workspace
 ln -s "$1" /out/node_modules/host
 ln -s "$2" /out/node_modules/dangling`, "sh", hostFile, strings.Repeat("target", 100)}
	out := run(t, in)
	want := map[string]struct{ mode, bytes string }{
		"node_modules/.bin/tool":      {"120000", "../tool"},
		"node_modules/workspace":      {"120000", "package"},
		"node_modules/host":           {"120000", hostFile},
		"node_modules/dangling":       {"120000", strings.Repeat("target", 100)},
		"node_modules/tool":           {"100755", "executable"},
		"node_modules/package/source": {"100644", "workspace"},
	}
	if len(out.Outputs) != len(want) {
		t.Fatalf("traversed symlink or missing output: %+v", out.Outputs)
	}
	for _, output := range out.Outputs {
		expected, ok := want[output.Path]
		if !ok || output.Mode != expected.mode || string(output.Bytes) != expected.bytes {
			t.Fatalf("output: %+v", output)
		}
	}
}

func TestReadRegularOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dir, "nested", "custom-report.json"), "report bytes")
	out, err := sandbox.ReadRegularOutput(dir, "nested/custom-report.json")
	if err != nil || out.Path != "nested/custom-report.json" || out.Mode != "100644" || string(out.Bytes) != "report bytes" {
		t.Fatalf("regular report: %+v %v", out, err)
	}
	if err := os.Symlink("nested", filepath.Join(dir, "alias")); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"alias/custom-report.json", "fifo", "nested", "../escape", "/absolute", "nested/../nested/custom-report.json"} {
		if out, err := sandbox.ReadRegularOutput(dir, path); err == nil || out.Bytes != nil {
			t.Fatalf("nonregular or invalid report %q accepted: %+v %v", path, out, err)
		}
	}
	if _, err := sandbox.ReadRegularOutput("/dev", "null"); err == nil {
		t.Fatal("device Report accepted")
	}
}

func TestReferenceArtifactParity(t *testing.T) {
	data, err := os.ReadFile("testdata/artifact.json")
	if err != nil {
		t.Fatal(err)
	}
	var expected []struct {
		Path   string
		Mode   string
		Bytes  string
		Digest string
	}
	if err := json.Unmarshal(data, &expected); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	for _, name := range []string{"node_modules/.bin", "node_modules/package"} {
		if err := os.MkdirAll(filepath.Join(dir, name), 0755); err != nil {
			t.Fatal(err)
		}
	}
	tool := filepath.Join(dir, "node_modules/package/tool")
	write(t, tool, "executable")
	if err := os.Chmod(tool, 0755); err != nil {
		t.Fatal(err)
	}
	for path, target := range map[string]string{"node_modules/.bin/tool": "../package/tool", "node_modules/workspace": "package", "node_modules/absolute": "/private/host-only-input"} {
		if err := os.Symlink(target, filepath.Join(dir, path)); err != nil {
			t.Fatal(err)
		}
	}
	outputs, err := sandbox.CollectOutputs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(outputs) != len(expected) {
		t.Fatalf("entries: %+v", outputs)
	}
	for i, output := range outputs {
		want := expected[i]
		if output.Path != want.Path || output.Mode != want.Mode || string(output.Bytes) != want.Bytes || fmt.Sprintf("%x", sha256.Sum256(output.Bytes)) != want.Digest {
			t.Fatalf("reference mismatch: %+v != %+v", output, want)
		}
	}
}

func TestImageOwnedProgramIsNotShadowed(t *testing.T) {
	in := invocation(t, `set -eu; test ! -e /verifier; printf ok > /out/report.json`)
	in.VerifierDir = ""
	if got := string(run(t, in).Outputs[0].Bytes); got != "ok" {
		t.Fatalf("report = %q", got)
	}
	mounted := invocation(t, `set -eu; test -d /verifier; printf ok > /out/report.json`)
	run(t, mounted)
}
