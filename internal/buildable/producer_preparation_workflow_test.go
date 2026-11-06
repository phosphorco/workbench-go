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
	if err := os.Symlink(filepath.Join(root, "failure.py"), filepath.Join(root, "link.py")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "real", "ok.py"), []byte("pass\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "linkdir")); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ interpreter, script string }{
		{"", ""}, {"python3", ""}, {"", "failure.py"}, {"sh", "failure.py"},
		{"python3", filepath.Join(root, "failure.py")}, {"python3", "../outside.py"},
		{"python3", "link.py"}, {"python3", "linkdir/ok.py"},
	} {
		if output, err := invoke(tc.interpreter, tc.script); err == nil {
			t.Errorf("accepted %q %q: %s", tc.interpreter, tc.script, strings.TrimSpace(output))
		}
	}
}
