package buildable_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func preparationWorkflow(t *testing.T) (string, map[string]string) {
	t.Helper()
	data, err := os.ReadFile("../../.github/workflows/workbench-buildable-promotion.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		On struct {
			WorkflowCall struct {
				Inputs map[string]struct {
					Default string `yaml:"default"`
				} `yaml:"inputs"`
			} `yaml:"workflow_call"`
		} `yaml:"on"`
		Jobs map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []struct {
				Name, If, Run, Shell string
				Env                  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &workflow); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"producer-prepare-script", "producer-prepare-interpreter"} {
		input, ok := workflow.On.WorkflowCall.Inputs[key]
		if !ok || input.Default != "" {
			t.Fatalf("%s must default to omission", key)
		}
	}
	job := workflow.Jobs["build"]
	if job.Permissions["contents"] != "read" {
		t.Fatal("build must retain read-only contents")
	}
	prepare, build := -1, -1
	for index, step := range job.Steps {
		if step.Name == "Prepare producer explicitly" {
			prepare = index
		}
		if step.Name == "Build declared platform into the local candidate" {
			build = index
		}
	}
	if prepare < 0 || build <= prepare {
		t.Fatal("preparation must precede build")
	}
	step := job.Steps[prepare]
	if step.If != "inputs.producer-prepare-interpreter != '' || inputs.producer-prepare-script != ''" || step.Shell != "bash" {
		t.Fatal("unexpected optional preparation condition")
	}
	return step.Run, step.Env
}

func TestProducerPreparationWorkflowExecutesConfinedScript(t *testing.T) {
	run, env := preparationWorkflow(t)
	if env["PREPARE_INTERPRETER"] != "${{ inputs.producer-prepare-interpreter }}" || env["PREPARE_SCRIPT"] != "${{ inputs.producer-prepare-script }}" {
		t.Fatal("workflow input mapping changed")
	}
	root := t.TempDir()
	invoke := func(interpreter, script string) (string, error) {
		cmd := exec.Command("bash", "-e", "-c", run)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GITHUB_WORKSPACE="+root, "PREPARE_INTERPRETER="+interpreter, "PREPARE_SCRIPT="+script)
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	for _, tc := range []struct{ interpreter, name, contents string }{
		{"python3", "prepare $(touch injected) space.py", "from pathlib import Path\nPath('python-ok').write_text('ok')\n"},
		{"bash", "prepare $(touch injected) space.sh", "printf ok > bash-ok\n"},
	} {
		if err := os.WriteFile(filepath.Join(root, tc.name), []byte(tc.contents), 0o644); err != nil {
			t.Fatal(err)
		}
		if output, err := invoke(tc.interpreter, tc.name); err != nil {
			t.Fatalf("%s: %v\n%s", tc.interpreter, err, output)
		}
	}
	for _, name := range []string{"python-ok", "bash-ok"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "injected")); !os.IsNotExist(err) {
		t.Fatal("script path was interpreted as shell")
	}
	if err := os.WriteFile(filepath.Join(root, "failure.py"), []byte("raise SystemExit(7)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke("python3", "failure.py"); err == nil {
		t.Fatal("failed preparation was accepted")
	}
	marker := filepath.Join(root, "forbidden-marker")
	pythonPayload := "open('forbidden-marker', 'w').write('executed')\n"
	if err := os.WriteFile(filepath.Join(root, "payload.py"), []byte(pythonPayload), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "payload.sh"), []byte("printf executed > forbidden-marker\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "payload.py"), filepath.Join(root, "link.py")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real", "ok.py"), []byte(pythonPayload), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root), "outside-"+filepath.Base(root)+".py")
	if err := os.WriteFile(outside, []byte(pythonPayload), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	for _, tc := range []struct{ interpreter, script, diagnostic string }{
		{"", "", "invalid producer preparation command"},
		{"python3", "", "invalid producer preparation command"},
		{"", "payload.py", "invalid producer preparation command"},
		{"sh", "payload.sh", "invalid producer preparation command"},
		{"python3", filepath.Join(root, "payload.py"), "invalid producer preparation command"},
		{"python3", filepath.Join("..", filepath.Base(outside)), "invalid producer preparation command"},
		{"python3", "link.py", "producer preparation script must be a regular checkout file without symlinks"},
		{"python3", "linkdir/ok.py", "producer preparation script must be a regular checkout file without symlinks"},
	} {
		_ = os.Remove(marker)
		output, err := invoke(tc.interpreter, tc.script)
		if err == nil || !strings.Contains(output, tc.diagnostic) {
			t.Errorf("refusal %q %q: %v %s", tc.interpreter, tc.script, err, strings.TrimSpace(output))
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Errorf("forbidden payload executed for %q %q", tc.interpreter, tc.script)
		}
	}
}

func TestProducerPreparationWorkflowIsolatesValidatorImports(t *testing.T) {
	run, _ := preparationWorkflow(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "subprocess.py"), []byte("raise SystemExit(0)\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "selected.py"), []byte("open('selected-marker', 'w').write('executed')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	invoke := func(script string) (string, error) {
		cmd := exec.Command("bash", "-e", "-c", run)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GITHUB_WORKSPACE="+root, "PREPARE_INTERPRETER=python3", "PREPARE_SCRIPT="+script)
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	if output, err := invoke("selected.py"); err != nil {
		t.Fatalf("selected preparation: %v %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(root, "selected-marker")); err != nil {
		t.Fatal("selected preparation silently skipped")
	}
	outside := filepath.Join(filepath.Dir(root), "outside-shadow-"+filepath.Base(root)+".py")
	if err := os.WriteFile(outside, []byte("open('forbidden-marker', 'w').write('executed')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	output, err := invoke(filepath.Join("..", filepath.Base(outside)))
	if err == nil || !strings.Contains(output, "invalid producer preparation command") {
		t.Fatalf("invalid input did not reach validation: %v %s", err, output)
	}
	if _, err := os.Stat(filepath.Join(root, "forbidden-marker")); !os.IsNotExist(err) {
		t.Fatal("outside preparation executed")
	}
}
