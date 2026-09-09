package contexthook

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
)

func TestPythonShellCorpusThroughPublicNormalizers(t *testing.T) {
	const root = "/repo"
	tests := []struct {
		name      string
		tool      string
		command   string
		workdir   string
		exitCode  int
		resources []contextapi.ObservedResource
	}{
		{
			name:    "pathlib alias read",
			tool:    "Bash",
			command: `python3 -c 'from pathlib import Path as P; p=P("README.md"); p.read_text()'`,
			resources: []contextapi.ObservedResource{
				pythonResource("README.md", contextapi.ResourceRead),
			},
		},
		{
			name:    "open read and write modes",
			tool:    "Bash",
			command: `python3 -c 'open("in.txt", "rb").read(); open("out.txt", "w").write("x")'`,
			resources: []contextapi.ObservedResource{
				pythonResource("in.txt", contextapi.ResourceRead),
				pythonResource("out.txt", contextapi.ResourceWrite),
			},
		},
		{
			name:    "corpus assigned open alias",
			tool:    "Bash",
			command: `python3 -c 'p="internal/plan/report.go"; s=open(p).read(); open(p,"w").write(s)'`,
			resources: []contextapi.ObservedResource{
				pythonResource("internal/plan/report.go", contextapi.ResourceRead),
				pythonResource("internal/plan/report.go", contextapi.ResourceWrite),
			},
		},
		{
			name:    "append and exclusive modes",
			tool:    "Bash",
			command: `python3 -c 'open("append.txt", "ab").write("x"); open("exclusive.txt", "x").write("x")'`,
			resources: []contextapi.ObservedResource{
				pythonResource("append.txt", contextapi.ResourceWrite),
				pythonResource("exclusive.txt", contextapi.ResourceWrite),
			},
		},
		{
			name:    "plus mode is read and write",
			tool:    "Bash",
			command: `python3 -c 'open("update.txt", "r+").read()'`,
			resources: []contextapi.ObservedResource{
				pythonResource("update.txt", contextapi.ResourceRead),
				pythonResource("update.txt", contextapi.ResourceWrite),
			},
		},
		{
			name:    "path joinpath read",
			tool:    "Bash",
			command: `python3 -c 'from pathlib import Path; Path("dir").joinpath("file.txt").read_text()'`,
			resources: []contextapi.ObservedResource{
				pythonResource("dir/file.txt", contextapi.ResourceRead),
			},
		},
		{
			name:    "path slash join read",
			tool:    "Bash",
			command: `python3 -c 'from pathlib import Path; (Path("dir") / "file.txt").read_text()'`,
			resources: []contextapi.ObservedResource{
				pythonResource("dir/file.txt", contextapi.ResourceRead),
			},
		},
		{
			name:    "corpus full replace chain with plain suffix",
			tool:    "Bash",
			command: "python3 - <<'PY'\nfrom pathlib import Path\np=Path('.context/workbench-context/implementation.plan.pkl')\ns=p.read_text().replace('local trace =','local historyStore =').replace('module.produced(trace)','module.produced(historyStore)').replace('module.verified(trace)','module.verified(historyStore)').replace('engine; trace;','engine; historyStore;')\np.write_text(s)\nPY\n/tmp/workbench-context-plan plan check .context/workbench-context/implementation.plan.pkl",
			resources: []contextapi.ObservedResource{
				pythonResource(".context/workbench-context/implementation.plan.pkl", contextapi.ResourceRead),
				pythonResource(".context/workbench-context/implementation.plan.pkl", contextapi.ResourceWrite),
			},
		},
		{
			name:    "compile keeps only nested read sink",
			tool:    "Bash",
			command: `python3 -c 'from pathlib import Path; compile(Path("examples/context/profile-provider.py").read_text(), "examples/context/profile-provider.py", "exec")'`,
			resources: []contextapi.ObservedResource{
				pythonResource("examples/context/profile-provider.py", contextapi.ResourceRead),
			},
		},
		{
			name:    "absolute interpreter",
			tool:    "Bash",
			command: `/usr/bin/python3 -c 'from pathlib import Path; Path("README.md").read_bytes()'`,
			resources: []contextapi.ObservedResource{
				pythonResource("README.md", contextapi.ResourceRead),
			},
		},
		{
			name:    "double quoted source removes shell escapes",
			tool:    "Bash",
			command: `python3 -c "from pathlib import Path; Path(\"README.md\").read_text()"`,
			resources: []contextapi.ObservedResource{
				pythonResource("README.md", contextapi.ResourceRead),
			},
		},
		{
			name:    "explicit relative workdir",
			tool:    "exec_command",
			command: `python3 -c 'from pathlib import Path; Path("README.md").read_text()'`,
			workdir: "nested",
			resources: []contextapi.ObservedResource{
				pythonResource("nested/README.md", contextapi.ResourceRead),
			},
		},
		{
			name:     "failure preserves intent as unknown",
			tool:     "Bash",
			command:  `python3 -c 'from pathlib import Path; p=Path("README.md"); p.write_text("x"); p.read_text()'`,
			exitCode: 1,
			resources: []contextapi.ObservedResource{
				pythonResource("README.md", contextapi.ResourceWrite),
				pythonResource("README.md", contextapi.ResourceRead),
			},
		},
	}

	for _, harness := range []contextapi.Harness{contextapi.HarnessClaudeCode, contextapi.HarnessCodex} {
		for _, test := range tests {
			t.Run(string(harness)+"/"+test.name, func(t *testing.T) {
				normalized := normalizePythonPublic(t, harness, root, test.tool, test.command, test.workdir, test.exitCode)
				if !reflect.DeepEqual(normalized.Observation.Resources, test.resources) {
					t.Fatalf("resources = %#v, want %#v; reasons=%#v", normalized.Observation.Resources, test.resources, normalized.Reasons)
				}
				if len(normalized.Observation.Selectors) != 0 {
					t.Fatalf("selectors = %#v, want none", normalized.Observation.Selectors)
				}
			})
		}
	}
}

func TestPythonShellUnsupportedProgramsProduceNoResources(t *testing.T) {
	const root = "/repo"
	tests := []struct {
		name    string
		tool    string
		command string
		workdir string
	}{
		{name: "environment printing", tool: "Bash", command: `python3 -c 'import os; print("README.md", os.environ.get("FILE"))'`},
		{name: "shadowed pathlib", tool: "Bash", command: `python3 -c 'from pathlib import Path; Path=str; Path("README.md").read_text()'`},
		{name: "dead function", tool: "Bash", command: "python3 -c 'from pathlib import Path\ndef edit():\n    Path(\"README.md\").read_text()'"},
		{name: "dynamic path", tool: "Bash", command: `python3 -c 'from pathlib import Path; Path("prefix-" + input()).read_text()'`},
		{name: "glob", tool: "Bash", command: `python3 -c 'from pathlib import Path; Path(".").glob("*")'`},
		{name: "chdir", tool: "Bash", command: `python3 -c 'import os; os.chdir("nested"); open("README.md").read()'`},
		{name: "unquoted expanding heredoc", tool: "Bash", command: "python3 - <<PY\nfrom pathlib import Path\nPath(\"$TARGET\").read_text()\nPY"},
		{name: "out of scope", tool: "Bash", command: `python3 -c 'from pathlib import Path; Path("../../secret").read_text()'`},
		{name: "unsupported flag", tool: "Bash", command: `python3 -u -c 'from pathlib import Path; Path("README.md").read_text()'`},
		{name: "python is not first", tool: "Bash", command: `echo ready; python3 -c 'from pathlib import Path; Path("README.md").read_text()'`},
		{name: "shadowed builtin open", tool: "Bash", command: `python3 -c 'open=print; open("README.md").read()'`},
		{name: "valid sink then unsupported exec", tool: "Bash", command: `python3 -c 'from pathlib import Path; Path("README.md").read_text(); exec("pass")'`},
		{name: "relative workdir escapes root", tool: "exec_command", workdir: "../outside", command: `python3 -c 'from pathlib import Path; Path("README.md").read_text()'`},
		{name: "absolute workdir escapes root", tool: "exec_command", workdir: "/outside", command: `python3 -c 'from pathlib import Path; Path("README.md").read_text()'`},
		{name: "absolute slash join escapes root", tool: "Bash", command: `python3 -c 'from pathlib import Path; (Path("nested") / "/outside/file").read_text()'`},
		{name: "absolute joinpath escapes root", tool: "Bash", command: `python3 -c 'from pathlib import Path; Path("nested").joinpath("/outside/file").read_text()'`},
		{name: "fd3 heredoc", tool: "Bash", command: "python3 - 3<<'PY'\nfrom pathlib import Path\nPath(\"README.md\").read_text()\nPY"},
		{name: "invalid unicode escape", tool: "Bash", command: `python3 -c 'from pathlib import Path; Path("\UFFFFFFFF").read_text()'`},
		{name: "malformed numeric name", tool: "Bash", command: `python3 -c '123abc'`},
		{name: "malformed valid sink then invalid assignment", tool: "Bash", command: `python3 -c 'open("x").read(), None=Path("x"); None.read_text()'`},
		{name: "None is not assignable", tool: "Bash", command: `python3 -c 'from pathlib import Path; None=Path("x"); None.read_text()'`},
		{name: "reserved keyword argument on Path", tool: "Bash", command: `python3 -c 'from pathlib import Path; Path("README.md").read_text(None="x")'`},
		{name: "reserved keyword argument on open", tool: "Bash", command: `python3 -c 'open("README.md", mode="r", class="x").read()'`},
		{name: "positional after keyword", tool: "Bash", command: `python3 -c 'open("x", mode="r", "extra").read()'`},
	}

	for _, harness := range []contextapi.Harness{contextapi.HarnessClaudeCode, contextapi.HarnessCodex} {
		for _, test := range tests {
			t.Run(string(harness)+"/"+test.name, func(t *testing.T) {
				normalized := normalizePythonPublic(t, harness, root, test.tool, test.command, test.workdir, 0)
				if len(normalized.Observation.Resources) != 0 || len(normalized.Observation.Selectors) != 0 {
					t.Fatalf("observation = %#v, selectors = %#v; want no inferred attention", normalized.Observation.Resources, normalized.Observation.Selectors)
				}
			})
		}
	}
}

func TestPythonShellBoundsAndMalformedHeredocStayAllOrNothing(t *testing.T) {
	const root = "/repo"
	longSource := "python3 -c 'from pathlib import Path; p=Path(\"README.md\"); " + strings.Repeat(" ", 64*1024) + "p.read_text()'"
	depthSource := "python3 -c 'from pathlib import Path; Path(" + strings.Repeat("(", 40) + `"README.md"` + strings.Repeat(")", 40) + ").read_text()'"
	tokenSource := "python3 -c 'from pathlib import Path; " + strings.Repeat(`p="x";`, 3000) + `p.read_text()'`
	cases := []struct {
		name    string
		command string
	}{
		{name: "source bound", command: longSource},
		{name: "nesting bound", command: depthSource},
		{name: "token bound", command: tokenSource},
		{name: "binding bound", command: pythonBindingLimitCommand()},
		{name: "derived string bound", command: pythonDoublingCommand()},
		{name: "partial heredoc delimiter", command: "python3 - <<-PY\nfrom pathlib import Path\nPath(\"README.md\").read_text()\nPY"},
		{name: "malformed source", command: `python3 -c 'from pathlib import Path; Path("README.md").read_text('`},
		{name: "unclosed string", command: `python3 -c 'from pathlib import Path; Path("README.md).read_text()'`},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			limits := DefaultInputLimits()
			if test.name == "source bound" {
				limits.MaxShellBytes = 128 * 1024
			}
			normalized := normalizePythonPublicWithLimits(t, contextapi.HarnessCodex, root, "Bash", test.command, "", 0, limits)
			if len(normalized.Observation.Resources) != 0 {
				t.Fatalf("resources = %#v, want all-or-nothing empty", normalized.Observation.Resources)
			}
		})
	}
}

func TestPythonShellHexEscapeDecodesToUTF8(t *testing.T) {
	normalized := normalizePythonPublic(t, contextapi.HarnessCodex, "/repo", "Bash", `python3 -c 'from pathlib import Path; Path("\xFF").read_text()'`, "", 0)
	want := []contextapi.ObservedResource{pythonResource("ÿ", contextapi.ResourceRead)}
	if !reflect.DeepEqual(normalized.Observation.Resources, want) {
		t.Fatalf("resources = %#v, want %#v; reasons=%#v", normalized.Observation.Resources, want, normalized.Reasons)
	}
}

func TestPythonShellExplicitInvalidWorkdirProducesNoResources(t *testing.T) {
	for _, test := range []struct {
		name  string
		value any
	}{
		{name: "empty", value: ""},
		{name: "null", value: nil},
		{name: "number", value: 17},
		{name: "array", value: []string{"nested"}},
		{name: "NUL path", value: "/repo/\x00nested"},
		{name: "NUL path with parent", value: "/repo/\x00nested/.."},
	} {
		t.Run(test.name, func(t *testing.T) {
			normalized := normalizePythonPublicWithWorkdir(t, contextapi.HarnessCodex, "/repo", "exec_command", `python3 -c 'from pathlib import Path; Path("README.md").read_text()'`, test.value, true, DefaultInputLimits(), 0)
			if len(normalized.Observation.Resources) != 0 || len(normalized.Observation.Selectors) != 0 {
				t.Fatalf("observation = %#v, selectors = %#v; want no inferred attention", normalized.Observation.Resources, normalized.Observation.Selectors)
			}
		})
	}
}

func TestPythonShellNULHookCwdProducesNoResources(t *testing.T) {
	normalized := normalizePythonPublicWithWorkdir(t, contextapi.HarnessCodex, "/repo/\x00scope", "Bash", `python3 -c 'from pathlib import Path; Path("README.md").read_text()'`, "", false, DefaultInputLimits(), 0)
	if len(normalized.Observation.Resources) != 0 || len(normalized.Observation.Selectors) != 0 {
		t.Fatalf("observation = %#v, selectors = %#v; want no inferred attention", normalized.Observation.Resources, normalized.Observation.Selectors)
	}
}

func TestPythonShellWordLimitAppliesToTrailingCommands(t *testing.T) {
	data, err := json.Marshal(map[string]any{
		"session_id":      "python-session",
		"cwd":             "/repo",
		"hook_event_name": "PostToolUse",
		"tool_name":       "Bash",
		"tool_input": map[string]string{
			"command": `python3 -c 'from pathlib import Path; Path("README.md").read_text()'; echo one two`,
		},
		"tool_response": map[string]any{"exit_code": 0},
	})
	if err != nil {
		t.Fatal(err)
	}
	limits := DefaultInputLimits()
	limits.MaxShellWords = 5
	hook, err := DecodeCodexHookWithLimits(context.Background(), data, limits)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NormalizeCodexHook(hook, enabledActivation("/repo"), time.Unix(10, 0)); err == nil || !errors.Is(err, ErrInputLimit) {
		t.Fatalf("Python shell word limit error = %v, want ErrInputLimit", err)
	}
}

func FuzzNormalizePythonRepresentativeSources(f *testing.F) {
	for _, seed := range []string{
		`from pathlib import Path; Path("README.md").read_text()`,
		`open("in.txt", "rb").read(); open("out.txt", "w").write("x")`,
		`from pathlib import Path
p=Path('.context/workbench-context/implementation.plan.pkl')
s=p.read_text().replace('local trace =','local historyStore =').replace('module.produced(trace)','module.produced(historyStore)').replace('module.verified(trace)','module.verified(historyStore)').replace('engine; trace;','engine; historyStore;')
p.write_text(s)`,
		`print("README.md")`,
		`123abc`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, source string) {
		const maxFuzzSourceBytes = 8 * 1024
		if len(source) > maxFuzzSourceBytes {
			source = source[:maxFuzzSourceBytes]
		}
		command := "python3 -c '" + strings.ReplaceAll(source, "'", "'\"'\"'") + "'"
		normalized := normalizePythonPublicWithLimits(t, contextapi.HarnessCodex, "/repo", "Bash", command, "", 0, DefaultInputLimits())
		if len(normalized.Observation.Resources) > pythonIntentMaxIntents {
			t.Fatalf("returned %d resources, want at most %d", len(normalized.Observation.Resources), pythonIntentMaxIntents)
		}
		for index, resource := range normalized.Observation.Resources {
			if resource.Kind != contextapi.ResourceFile || resource.Confidence != contextapi.ConfidenceInferred || resource.Outcome != contextapi.ResourceOutcomeUnknown {
				t.Fatalf("resource[%d] metadata = %#v, want file/inferred/unknown", index, resource)
			}
			if strings.HasPrefix(resource.Path, "/") {
				t.Fatalf("resource[%d] path = %q, want nonabsolute", index, resource.Path)
			}
			for _, component := range strings.Split(resource.Path, "/") {
				if component == ".." {
					t.Fatalf("resource[%d] path = %q, want no parent escape", index, resource.Path)
				}
			}
		}
	})
}

func pythonBindingLimitCommand() string {
	var source strings.Builder
	source.WriteString(`from pathlib import Path; p=Path("stale.txt"); `)
	for index := 0; index < 300; index++ {
		source.WriteString("b")
		source.WriteString(strconv.Itoa(index))
		source.WriteString(`="x"; `)
	}
	source.WriteString(`p.read_text()`)
	return "python3 -c '" + source.String() + "'"
}

func pythonDoublingCommand() string {
	var source strings.Builder
	source.WriteString(`from pathlib import Path; s="x"; `)
	for index := 0; index < 20; index++ {
		source.WriteString("s=s+s; ")
	}
	source.WriteString(`Path(s).read_text()`)
	return "python3 -c '" + source.String() + "'"
}

func pythonResource(path string, operation contextapi.ResourceOperation) contextapi.ObservedResource {
	return contextapi.ObservedResource{
		Path: path, Kind: contextapi.ResourceFile, Operation: operation,
		Outcome: contextapi.ResourceOutcomeUnknown, Confidence: contextapi.ConfidenceInferred,
	}
}

func normalizePythonPublic(t *testing.T, harness contextapi.Harness, root, tool, command, workdir string, exitCode int) Normalized {
	return normalizePythonPublicWithLimits(t, harness, root, tool, command, workdir, exitCode, DefaultInputLimits())
}

func normalizePythonPublicWithLimits(t *testing.T, harness contextapi.Harness, root, tool, command, workdir string, exitCode int, limits InputLimits) Normalized {
	return normalizePythonPublicWithWorkdir(t, harness, root, tool, command, workdir, workdir != "", limits, exitCode)
}

func normalizePythonPublicWithWorkdir(t *testing.T, harness contextapi.Harness, root, tool, command string, workdir any, includeWorkdir bool, limits InputLimits, exitCode int) Normalized {
	t.Helper()
	inputField := "command"
	if tool == "exec_command" {
		inputField = "cmd"
	}
	toolInput := map[string]any{inputField: command}
	if includeWorkdir {
		toolInput["workdir"] = workdir
	}
	payload := map[string]any{
		"session_id":      "python-session",
		"cwd":             root,
		"hook_event_name": map[contextapi.Harness]string{contextapi.HarnessClaudeCode: "PostToolBatch", contextapi.HarnessCodex: "PostToolUse"}[harness],
		"tool_name":       tool,
		"tool_input":      toolInput,
		"tool_response":   map[string]any{"exit_code": exitCode},
		"tool_use_id":     "python-call",
	}
	if harness == contextapi.HarnessClaudeCode {
		delete(payload, "tool_name")
		delete(payload, "tool_input")
		delete(payload, "tool_response")
		delete(payload, "tool_use_id")
		payload["prompt_id"] = "python-prompt"
		payload["tool_calls"] = []any{map[string]any{
			"tool_name":     tool,
			"tool_input":    toolInput,
			"tool_response": map[string]any{"exit_code": exitCode},
			"tool_use_id":   "python-call",
		}}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	var hook HookDTO
	if harness == contextapi.HarnessClaudeCode {
		hook, err = DecodeClaudeHookWithLimits(context.Background(), data, limits)
	} else {
		hook, err = DecodeCodexHookWithLimits(context.Background(), data, limits)
	}
	if err != nil {
		t.Fatal(err)
	}
	var normalized Normalized
	if harness == contextapi.HarnessClaudeCode {
		normalized, err = NormalizeClaudeHook(hook, enabledActivation(root), time.Unix(10, 0))
	} else {
		normalized, err = NormalizeCodexHook(hook, enabledActivation(root), time.Unix(10, 0))
	}
	if err != nil {
		t.Fatal(err)
	}
	return normalized
}
