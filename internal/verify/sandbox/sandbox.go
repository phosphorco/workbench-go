package sandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Upstream struct {
	Name    string
	HostDir string
}

// Invocation grants only the fixed mounts in the Run contract. WorkDir is
// already staged by the caller; ScratchDir is empty for strong toolchains;
// VerifierDir is empty when the program lives in the image, so nothing
// shadows the image's /verifier.
type Invocation struct {
	Image       string
	Command     []string
	UnitID      string
	Role        string
	WorkDir     string
	VerifierDir string
	OutputDir   string
	Upstream    []Upstream
	ScratchDir  string
	Network     string
	Timeout     time.Duration
}

type Output struct {
	Path  string
	Mode  string
	Bytes []byte
}

// Outcome retains process evidence even when Execute returns a tool failure.
type Outcome struct {
	ExitCode int
	Stdout   []byte
	Stderr   string
	TimedOut bool
	Outputs  []Output
}

// ToolFailure makes a failed execution distinguishable from subject diagnostics.
type ToolFailure struct {
	Reason string
	Cause  error
}

func (e *ToolFailure) Error() string { return e.Reason }
func (e *ToolFailure) Unwrap() error { return e.Cause }

var pinnedImage = regexp.MustCompile(`^[^\s@]+@sha256:[0-9a-f]{64}$`)
var mountName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]*$`)

const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

// Execute runs one container, removes it before collecting outputs,
// and returns tool failures for nonzero exits, timeouts, and refused outputs.
func Execute(ctx context.Context, invocation Invocation) (out Outcome, err error) {
	out.ExitCode = -1
	args, err := createArgs(invocation)
	if err != nil {
		return out, failure("invalid invocation", err)
	}
	ctx, cancel := context.WithTimeout(ctx, invocation.Timeout)
	defer cancel()
	defer func() {
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			out.TimedOut = true
		}
	}()
	imageEnv, err := imagePath(ctx, invocation.Image)
	if err != nil {
		return out, failure("image unavailable", err)
	}
	name := "verify-" + rand.Text()
	args = append([]string{"create", "--name", name}, args...)
	args = append(args, "--entrypoint", "env", invocation.Image, "-i", "PATH="+imageEnv,
		"HOME=/tmp", "VERIFY_UNIT="+invocation.UnitID, "VERIFY_ROLE="+invocation.Role)
	args = append(args, invocation.Command...)
	removed := false
	defer func() {
		if !removed {
			if cleanupErr := remove(name); cleanupErr != nil {
				err = failure("container cleanup failed", errors.Join(err, cleanupErr))
			}
		}
	}()
	if _, err := docker(ctx, nil, args...); err != nil {
		out.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
		return out, failure("container creation failed", err)
	}
	var stdin bytes.Buffer
	encoder := json.NewEncoder(&stdin)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(struct {
		UnitID string `json:"unitId"`
	}{invocation.UnitID}); err != nil {
		return out, failure("encode stdin", err)
	}
	input := bytes.TrimSuffix(stdin.Bytes(), []byte("\n"))
	ran, runErr := docker(ctx, input, "start", "--attach", "--interactive", name)
	out.Stdout, out.Stderr = ran.Stdout, ran.Stderr
	out.TimedOut = errors.Is(ctx.Err(), context.DeadlineExceeded)
	if ctx.Err() != nil {
		return out, failure("container interrupted", ctx.Err())
	}
	state, inspectErr := docker(ctx, nil, "inspect", "--format", "{{json .State}}", name)
	if inspectErr != nil {
		return out, failure("inspect container", inspectErr)
	}
	var status struct {
		ExitCode int
		Error    string
		Running  bool
	}
	if err := json.Unmarshal(state.Stdout, &status); err != nil {
		return out, failure("decode container state", err)
	}
	out.ExitCode = status.ExitCode
	if status.Running || status.Error != "" {
		return out, failure("container execution failed", fmt.Errorf("running=%t: %s", status.Running, status.Error))
	}
	if runErr != nil && status.ExitCode == 0 {
		return out, failure("docker attach failed", runErr)
	}
	if err := remove(name); err != nil {
		return out, failure("container cleanup failed", err)
	}
	removed = true
	if out.ExitCode != 0 {
		return out, failure(fmt.Sprintf("container exited %d", out.ExitCode), runErr)
	}
	out.Outputs, err = CollectOutputs(invocation.OutputDir)
	if err != nil {
		return out, failure("output refused", err)
	}
	return out, nil
}

func failure(reason string, err error) *ToolFailure {
	if err != nil {
		reason += ": " + err.Error()
	}
	return &ToolFailure{Reason: reason, Cause: err}
}

func createArgs(in Invocation) ([]string, error) {
	if !pinnedImage.MatchString(in.Image) {
		return nil, errors.New("image must be pinned by sha256 digest")
	}
	if len(in.Command) == 0 || in.Command[0] == "" {
		return nil, errors.New("command is empty")
	}
	if in.Role != "run" && in.Role != "select" {
		return nil, errors.New("role must be run or select")
	}
	if in.Role == "select" && in.UnitID != "*" {
		return nil, errors.New("select unitId must be *")
	}
	if in.Timeout <= 0 {
		return nil, errors.New("timeout must be positive")
	}
	network := "none"
	if in.Network == "unrestricted" {
		network = "bridge"
	} else if in.Network != "none" {
		return nil, errors.New("network must be none or unrestricted")
	}
	args := []string{"-i", "--network", network, "--read-only", "--tmpfs", "/tmp:exec", "--user", fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid()), "--workdir", "/work"}
	type mount struct {
		host, target string
		writable     bool
	}
	mounts := []mount{{in.WorkDir, "/work", false}, {in.OutputDir, "/out", true}}
	if in.VerifierDir != "" {
		mounts = append(mounts, mount{in.VerifierDir, "/verifier", false})
	}
	seen := map[string]bool{}
	for _, up := range in.Upstream {
		if !mountName.MatchString(up.Name) || seen[up.Name] {
			return nil, errors.New("invalid or duplicate upstream name")
		}
		seen[up.Name] = true
		mounts = append(mounts, mount{up.HostDir, "/upstream/" + up.Name, false})
	}
	if in.ScratchDir != "" {
		mounts = append(mounts, mount{in.ScratchDir, "/scratch", true})
	}
	for _, m := range mounts {
		host, err := filepath.Abs(m.host)
		if err != nil || m.host == "" {
			return nil, errors.New("mount requires a host directory")
		}
		info, err := os.Stat(host)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", host)
		}
		fields := []string{"type=bind", "src=" + host, "dst=" + m.target}
		if !m.writable {
			fields = append(fields, "readonly", "bind-propagation=rprivate", "bind-recursive=readonly")
		}
		var encoded bytes.Buffer
		writer := csv.NewWriter(&encoded)
		if err := writer.Write(fields); err != nil {
			return nil, err
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return nil, err
		}
		args = append(args, "--mount", strings.TrimSuffix(encoded.String(), "\n"))
	}
	return args, nil
}

func imagePath(ctx context.Context, image string) (string, error) {
	args := []string{"image", "inspect", "--format", "{{json .Config.Env}}", image}
	ran, err := docker(ctx, nil, args...)
	if err != nil {
		if _, err := docker(ctx, nil, "pull", "--quiet", image); err != nil {
			return "", err
		}
		ran, err = docker(ctx, nil, args...)
		if err != nil {
			return "", err
		}
	}
	var env []string
	if err := json.Unmarshal(ran.Stdout, &env); err != nil {
		return "", err
	}
	for _, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			return strings.TrimPrefix(entry, "PATH="), nil
		}
	}
	return defaultPath, nil
}

func docker(ctx context.Context, stdin []byte, args ...string) (Outcome, error) {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = bytes.NewReader(stdin)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	out := Outcome{ExitCode: -1, Stdout: stdout.Bytes(), Stderr: stderr.String()}
	if cmd.ProcessState != nil {
		out.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		err = fmt.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(out.Stderr))
	}
	return out, err
}

func remove(name string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, err := docker(ctx, nil, "rm", "--force", name)
	return err
}

// CollectOutputs returns sorted files beneath dir, preserving symlink target
// text with mode 120000 without traversal. FIFOs, devices, and sockets are refused.
func CollectOutputs(dir string) ([]Output, error) {
	root, err := openOutputDirectory(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	outputs := []Output{}
	if err := collectDirectory(root, "", &outputs); err != nil {
		return nil, err
	}
	sort.Slice(outputs, func(i, j int) bool { return outputs[i].Path < outputs[j].Path })
	return outputs, nil
}

// ReadRegularOutput reads a Report or other regular output at a relative path.
// Symlinks in any path component and non-regular files are refused.
func ReadRegularOutput(dir, path string) (Output, error) {
	if !fs.ValidPath(path) || path == "." {
		return Output{}, fmt.Errorf("invalid relative output path: %q", path)
	}
	root, err := openOutputDirectory(dir)
	if err != nil {
		return Output{}, err
	}
	defer root.Close()
	components := strings.Split(path, "/")
	for i := range components {
		prefix := strings.Join(components[:i+1], "/")
		info, err := root.Lstat(prefix)
		if err != nil {
			return Output{}, fmt.Errorf("%s: %w", path, err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return Output{}, fmt.Errorf("%s: %s is a symlink", path, prefix)
		}
		if i < len(components)-1 && !info.IsDir() {
			return Output{}, fmt.Errorf("%s: %s is not a directory", path, prefix)
		}
	}
	return regularOutputAt(root, path)
}

// openOutputDirectory confines every later open to dir; dir itself must not be
// a symlink. Collection happens after the container has exited, so the
// Lstat-then-open sequence below is not racing a writer.
func openOutputDirectory(dir string) (*os.Root, error) {
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s is a symlink", dir)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	return os.OpenRoot(dir)
}

func collectDirectory(root *os.Root, prefix string, outputs *[]Output) error {
	name := "."
	if prefix != "" {
		name = strings.TrimSuffix(prefix, "/")
	}
	directory, err := root.Open(name)
	if err != nil {
		return err
	}
	entries, err := directory.ReadDir(-1)
	directory.Close()
	if err != nil {
		return err
	}
	for _, entry := range entries {
		path := prefix + entry.Name()
		info, err := root.Lstat(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		switch {
		case info.Mode()&fs.ModeSymlink != 0:
			target, err := root.Readlink(path)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			*outputs = append(*outputs, Output{Path: path, Mode: "120000", Bytes: []byte(target)})
		case info.IsDir():
			if err := collectDirectory(root, path+"/", outputs); err != nil {
				return err
			}
		default:
			output, err := regularOutputAt(root, path)
			if err != nil {
				return err
			}
			*outputs = append(*outputs, output)
		}
	}
	return nil
}

func regularOutputAt(root *os.Root, path string) (Output, error) {
	info, err := root.Lstat(path)
	if err != nil {
		return Output{}, fmt.Errorf("%s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return Output{}, fmt.Errorf("%s is not a regular file", path)
	}
	file, err := root.Open(path)
	if err != nil {
		return Output{}, fmt.Errorf("%s: %w", path, err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return Output{}, err
	}
	return regularOutput(file, path, opened)
}

func regularOutput(file *os.File, path string, info os.FileInfo) (Output, error) {
	if !info.Mode().IsRegular() {
		return Output{}, fmt.Errorf("%s is not a regular file", path)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return Output{}, err
	}
	mode := "100644"
	if info.Mode().Perm()&0100 != 0 {
		mode = "100755"
	}
	return Output{Path: path, Mode: mode, Bytes: data}, nil
}
