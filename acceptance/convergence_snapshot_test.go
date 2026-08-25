package acceptance

import (
	"strings"
	"testing"
)

func TestWorkbenchConvergenceNarrative(t *testing.T) {
	fixture := newNarrativeFixture(t, true)
	workbench := fixture.newWorkbench(t, "convergent", currentSubjectContract, currentAgentContract, "entry")
	prepared := fixture.runNarrativeSetup(t, workbench)
	if prepared.exitCode != 0 {
		t.Fatalf("prepare convergent Workbench exited %d:\n%s", prepared.exitCode, prepared.output)
	}
	command := fixture.runNarrativeSetup(t, workbench)
	if command.exitCode != 0 || !strings.Contains(command.output, "0 generated paths changed") {
		t.Fatalf("convergent setup exited %d:\n%s", command.exitCode, command.output)
	}
	narrative := renderSetupNarrative(t.Name(), "Identical second setup", "The identical request converges without changing an owned projection.", command, workbench)
	assertWorkbenchSnapshot(t, "convergence_snapshot_test.go.snap.md", narrative)
}
