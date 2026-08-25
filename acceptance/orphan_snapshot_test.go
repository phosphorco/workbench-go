package acceptance

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkbenchOrphanNarrative(t *testing.T) {
	fixture := newNarrativeFixture(t, false)
	workbench := fixture.newWorkbench(t, "orphan", currentSubjectContract, currentAgentContract, "entry")
	if err := os.Remove(filepath.Join(workbench, "workbench-subject.pkl")); err != nil {
		t.Fatal(err)
	}
	declared := fixture.runNarrativeSubjectWrite(t, workbench, "entry", "library")
	if declared.exitCode != 0 {
		t.Fatalf("declare initial Subject exited %d:\n%s", declared.exitCode, declared.output)
	}
	prepared := fixture.runNarrativeSetup(t, workbench)
	if prepared.exitCode != 0 {
		t.Fatalf("prepare Workbench with future orphan exited %d:\n%s", prepared.exitCode, prepared.output)
	}
	orphanPath := filepath.Join(workbench, "repos", "library")
	orphanBefore := narrativeTreeDigest(t, orphanPath)
	edited := fixture.runNarrativeSubjectWrite(t, workbench, "entry")
	if edited.exitCode != 0 {
		t.Fatalf("edit Subject for orphan report exited %d:\n%s", edited.exitCode, edited.output)
	}
	command := fixture.runNarrativeSetup(t, workbench)
	if command.exitCode != 0 {
		t.Fatalf("orphan-reporting setup exited %d:\n%s", command.exitCode, command.output)
	}
	for _, fact := range []string{"phosphorco/library", "repos/library"} {
		if !strings.Contains(command.output, fact) {
			t.Fatalf("orphan-reporting setup omitted %q:\n%s", fact, command.output)
		}
	}
	if orphanAfter := narrativeTreeDigest(t, orphanPath); orphanAfter != orphanBefore {
		t.Fatalf("setup changed preserved orphan checkout: before %s after %s", orphanBefore, orphanAfter)
	}
	observed := fixture.runNarrativeShell(t, workbench, "test -d repos/library && printf '%s\\n' 'Preserved checkout: repos/library still exists.'")
	if observed.exitCode != 0 {
		t.Fatalf("observe preserved orphan exited %d:\n%s", observed.exitCode, observed.output)
	}
	narrative := renderNarrative(t.Name(), workbench,
		narrativeStep{title: "Create the Subject", consequence: "The Subject declares both the entry repository and the library repository.", command: declared},
		narrativeStep{title: "Reconcile the Workbench", consequence: "The first real setup creates the complete two-repository Workbench.", command: prepared},
		narrativeStep{title: "Edit the declaration", consequence: "The Subject now declares only the entry repository.", command: edited},
		narrativeStep{title: "Reconcile the edited Subject", consequence: "The second real setup reports the removed library as an orphan.", command: command},
		narrativeStep{title: "Observe the preserved checkout", consequence: "A real filesystem observation confirms that setup did not delete the orphan checkout.", command: observed},
	)
	assertWorkbenchSnapshot(t, "orphan_snapshot_test.go.snap.md", narrative)
}
