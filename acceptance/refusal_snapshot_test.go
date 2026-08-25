package acceptance

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkbenchDirtyOtherBranchRefusalNarrative(t *testing.T) {
	fixture := newNarrativeFixture(t, true)
	workbench := fixture.newWorkbench(t, "unsafe", currentSubjectContract, currentAgentContract, "entry")
	prepared := fixture.runNarrativeSetup(t, workbench)
	if prepared.exitCode != 0 {
		t.Fatalf("prepare unsafe Workbench exited %d:\n%s", prepared.exitCode, prepared.output)
	}
	entry := filepath.Join(workbench, "pkg", "@workbench-entry")
	git(t, entry, "checkout", "main")
	source := filepath.Join(entry, "app", "src", "index.ts")
	writeFile(t, source, readFile(t, source)+"\n// dirty on the other branch\n")
	before := narrativeTreeDigest(t, workbench)
	command := fixture.runNarrativeSetup(t, workbench)
	if command.exitCode == 0 {
		t.Fatalf("dirty-other-branch setup unexpectedly succeeded:\n%s", command.output)
	}
	for _, fact := range []string{"dirty checkout", "local/package-scope-current"} {
		if !strings.Contains(command.output, fact) {
			t.Fatalf("dirty-other-branch refusal omitted %q:\n%s", fact, command.output)
		}
	}
	if after := narrativeTreeDigest(t, workbench); after != before {
		t.Fatalf("refused setup mutated the disposable Workbench: before %s after %s", before, after)
	}
	narrative := renderSetupNarrative(t.Name(), "Dirty checkout on another branch", "Setup refuses the unsafe branch switch and leaves the complete Git and filesystem state unchanged.", command, workbench)
	assertWorkbenchSnapshot(t, "refusal_snapshot_test.go.snap.md", narrative)
}
