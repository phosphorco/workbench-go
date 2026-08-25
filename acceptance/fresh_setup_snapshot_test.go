package acceptance

import (
	"strings"
	"testing"
)

func TestWorkbenchFreshSetupNarrative(t *testing.T) {
	fixture := newNarrativeFixture(t, true)
	workbench := fixture.newWorkbench(t, "fresh", currentSubjectContract, currentAgentContract, "entry")
	command := fixture.runNarrativeSetup(t, workbench)
	if command.exitCode != 0 {
		t.Fatalf("fresh setup exited %d:\n%s", command.exitCode, command.output)
	}
	for _, fact := range []string{"Workbench reconciled 2 repositories", "generated paths changed"} {
		if !strings.Contains(command.output, fact) {
			t.Fatalf("fresh setup omitted %q:\n%s", fact, command.output)
		}
	}
	narrative := renderSetupNarrative(t.Name(), "Fresh setup", "A fresh corrected PackageScope Subject assembles its two independently governed repositories into a Workbench.", command, workbench)
	assertWorkbenchSnapshot(t, "fresh_setup_snapshot_test.go.snap.md", narrative)
}
