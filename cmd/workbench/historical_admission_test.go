package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/evaluate"
)

func TestHistorical090LifecycleAdmission(t *testing.T) {
	const version = "0.9.0"
	const prefix = "package://github.com/phosphorco/workbench-go/releases/download/0.9.0/workbench@0.9.0#/"
	t.Run("commit plan", func(t *testing.T) {
		source := []byte("amends \"" + prefix + "WorkbenchCommitPlan.pkl\"\n")
		if _, err := releasedCommitPlanContract(source, version); err != nil {
			t.Fatalf("historical commit plan admission: %v", err)
		}
		wrong := []byte("amends \"package://github.com/phosphorco/workbench-go/releases/download/0.10.0/workbench@0.10.0#/WorkbenchCommitPlan.pkl\"\n")
		if _, err := releasedCommitPlanContract(wrong, version); err == nil || !strings.Contains(err.Error(), "want exact Workbench 0.9.0") {
			t.Fatalf("cross-version commit plan: %v", err)
		}
	})
	t.Run("snapshot", func(t *testing.T) {
		source := []byte("amends \"" + prefix + "WorkbenchSnapshot.pkl\"\n")
		if _, kind, err := releasedSnapshotContractFromSource(source); err != nil || kind != currentSnapshotContract {
			t.Fatalf("historical snapshot admission: kind %v, error %v", kind, err)
		}
	})
	t.Run("subject", func(t *testing.T) {
		root := t.TempDir()
		source := []byte("amends \"" + prefix + "WorkbenchSubject.pkl\"\n")
		if err := os.WriteFile(filepath.Join(root, "workbench-subject.pkl"), source, 0o644); err != nil {
			t.Fatal(err)
		}
		// The uninitialized evaluator is a boundary witness: admission must reach
		// evaluation, without acquiring Pkl or the network in this selector test.
		_, err := evaluateSubject(context.Background(), root, evaluate.Evaluator{}, version)
		if err == nil || !strings.Contains(err.Error(), "evaluator is uninitialized") {
			t.Fatalf("historical Subject did not reach evaluation: %v", err)
		}
	})
}

func TestHistorical010LifecycleAdmission(t *testing.T) {
	const version = "0.10.0"
	const prefix = "package://github.com/phosphorco/workbench-go/releases/download/0.10.0/workbench@0.10.0#/"
	t.Run("commit plan", func(t *testing.T) {
		if _, err := releasedCommitPlanContract([]byte("amends \""+prefix+"WorkbenchCommitPlan.pkl\"\n"), version); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("snapshot", func(t *testing.T) {
		if _, kind, err := releasedSnapshotContractFromSource([]byte("amends \"" + prefix + "WorkbenchSnapshot.pkl\"\n")); err != nil || kind != currentSnapshotContract {
			t.Fatalf("kind=%v err=%v", kind, err)
		}
	})
	t.Run("subject", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "workbench-subject.pkl"), []byte("amends \""+prefix+"WorkbenchSubject.pkl\"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := evaluateSubject(context.Background(), root, evaluate.Evaluator{}, version)
		if err == nil || !strings.Contains(err.Error(), "evaluator is uninitialized") {
			t.Fatalf("historical subject admission: %v", err)
		}
	})
}
