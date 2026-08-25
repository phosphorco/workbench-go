package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const updateWorkbenchSnapshots = "UPDATE_WORKBENCH_SNAPSHOTS"

type narrativeFixture struct {
	packageScopeFixture
}

func newNarrativeFixture(t *testing.T, entryIncludesLibrary bool) narrativeFixture {
	t.Helper()
	fixture := newPackageScopeFixture(t)
	createRemote(t, fixture.root, fixture.remotes, "library", fmt.Sprintf(`amends %q

packages {
  ["@workbench-library/shared"] {}
}
`, currentRepositoryContract), map[string]string{
		".gitignore":   "/package.json\n/tsconfig.json\n/dist/\n/node_modules/\n/bun.lock\n/.agents/skills/\n",
		"src/index.ts": "export const sharedMessage: string = \"narrative library\"\n",
	})
	createRemote(t, fixture.root, fixture.remotes, "entry", narrativeEntryDeclaration(entryIncludesLibrary), map[string]string{
		".gitignore":        "/app/package.json\n/app/tsconfig.json\n/app/dist/\n/app/node_modules/\n/tool/package.json\n/tool/tsconfig.json\n/tool/dist/\n/tool/node_modules/\n/.agents/skills/\n",
		"app/src/index.ts":  "export const entryMessage: string = \"narrative app\"\n",
		"tool/src/index.ts": "export const toolMessage: string = \"narrative tool\"\n",
	})
	return narrativeFixture{packageScopeFixture: fixture}
}

func narrativeSubject(entrypoints ...string) string {
	var entries strings.Builder
	for _, entrypoint := range entrypoints {
		fmt.Fprintf(&entries, "  %q\n", "https://github.com/phosphorco/"+entrypoint)
	}
	return fmt.Sprintf(`amends %q

workLine {
  branch = "local/package-scope-current"
  baseBranch = "main"
}
entrypoints {
%s}
`, currentSubjectContract, entries.String())
}

func narrativeEntryDeclaration(includeLibrary bool) string {
	include := ""
	if includeLibrary {
		include = `
includes {
  ["phosphorco/library"] {}
}
`
	}
	return fmt.Sprintf(`amends %q

scope = "@workbench-entry"
%s
packages {
  ["@workbench-entry/app"] {}
  ["@workbench-entry/tool"] {}
}
`, currentPackageScopeContract, include)
}

type narrativeCommand struct {
	console  string
	output   string
	exitCode int
}

type narrativeStep struct {
	title       string
	consequence string
	command     narrativeCommand
}

func (fixture narrativeFixture) runNarrativeShell(t *testing.T, root, console string) narrativeCommand {
	t.Helper()
	command := exec.Command("sh", "-c", console)
	command.Dir = root
	command.Env = fixture.environment
	output, err := command.CombinedOutput()
	if err == nil {
		return narrativeCommand{console: console, output: string(output)}
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("execute narrative shell command: %v\n%s", err, output)
	}
	return narrativeCommand{console: console, output: string(output), exitCode: exitError.ExitCode()}
}

func (fixture narrativeFixture) runNarrativeSubjectWrite(t *testing.T, root string, entrypoints ...string) narrativeCommand {
	t.Helper()
	console := "cat > workbench-subject.pkl <<'PKL'\n" + narrativeSubject(entrypoints...) + "PKL"
	return fixture.runNarrativeShell(t, root, console)
}

func (fixture narrativeFixture) runNarrativeSetup(t *testing.T, root string) narrativeCommand {
	t.Helper()
	command := exec.Command(fixture.binary, "setup")
	command.Dir = root
	command.Env = fixture.environment
	output, err := command.CombinedOutput()
	if err == nil {
		return narrativeCommand{console: "workbench setup", output: string(output)}
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("execute Workbench setup: %v\n%s", err, output)
	}
	return narrativeCommand{console: "workbench setup", output: string(output), exitCode: exitError.ExitCode()}
}

func renderSetupNarrative(testName, title, consequence string, command narrativeCommand, disposableRoot string) string {
	return renderNarrative(testName, disposableRoot, narrativeStep{title: title, consequence: consequence, command: command})
}

func renderNarrative(testName, disposableRoot string, steps ...narrativeStep) string {
	var story strings.Builder
	fmt.Fprintf(&story, "<!-- Update from the repository root: UPDATE_WORKBENCH_SNAPSHOTS=1 go -C tools/workbench-go test ./acceptance -run '^%s$' -count=1 -->\n", testName)
	story.WriteString("# Workbench setup narrative\n\n")
	for _, step := range steps {
		writeNarrativeStep(&story, step.title, step.consequence, step.command, disposableRoot)
	}
	return story.String()
}

func writeNarrativeStep(story *strings.Builder, title, consequence string, command narrativeCommand, disposableRoot string) {
	fmt.Fprintf(story, "## %s\n\n%s\n\n```console\n$ %s\n", title, consequence, command.console)
	output := strings.ReplaceAll(command.output, disposableRoot, "<workbench>")
	if output != "" {
		story.WriteString(strings.TrimRight(output, "\n"))
		story.WriteByte('\n')
	}
	if command.exitCode != 0 {
		fmt.Fprintf(story, "exit %d\n", command.exitCode)
	}
	story.WriteString("```\n\n")
}

func assertWorkbenchSnapshot(t *testing.T, path, actual string) {
	t.Helper()
	if os.Getenv(updateWorkbenchSnapshots) == "1" {
		if err := os.WriteFile(path, []byte(actual), 0o600); err != nil {
			t.Fatalf("update Workbench snapshot: %v", err)
		}
		return
	}
	expected, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read Workbench snapshot (update with %s=1): %v", updateWorkbenchSnapshots, err)
	}
	if string(expected) == actual {
		return
	}
	t.Fatalf("Workbench narrative snapshot differs (-expected +actual):\n%s", markdownDiff(t, expected, []byte(actual)))
}

func markdownDiff(t *testing.T, expected, actual []byte) string {
	t.Helper()
	directory := t.TempDir()
	expectedPath := filepath.Join(directory, "expected.snap.md")
	actualPath := filepath.Join(directory, "actual.snap.md")
	if err := os.WriteFile(expectedPath, expected, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(actualPath, actual, 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "diff", "--no-index", "--no-color", "--unified=3", "--", expectedPath, actualPath)
	output, err := command.CombinedOutput()
	if err == nil {
		return "(no textual diff)"
	}
	if exitError, ok := err.(*exec.ExitError); !ok || exitError.ExitCode() != 1 {
		t.Fatalf("render snapshot diff: %v\n%s", err, output)
	}
	diff := strings.ReplaceAll(string(output), expectedPath, "expected.snap.md")
	diff = strings.ReplaceAll(diff, actualPath, "actual.snap.md")
	return diff
}

func narrativeTreeDigest(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path != root {
			paths = append(paths, path)
		}
		return nil
	}); err != nil {
		t.Fatalf("observe disposable tree: %v", err)
	}
	slices.Sort(paths)
	hash := sha256.New()
	for _, path := range paths {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(hash, "%s\x00%s\x00", filepath.ToSlash(relative), info.Mode().String())
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				t.Fatal(err)
			}
			hash.Write([]byte(target))
		case info.Mode().IsRegular():
			contents, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			hash.Write(contents)
		}
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
