package acceptance

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/orphan"
	"github.com/phosphorco/workbench-go/internal/snapshot"
)

const snapshotPruneV1Commit = "0123456789abcdef0123456789abcdef01234567"

func TestSnapshotAndPruneV1PreserveConflictsAndRequireRecoverability(t *testing.T) {
	t.Run("snapshot origin conflict preserves checkout", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "repos", "workbench-fixture-library")
		writeFile(t, filepath.Join(path, "source.txt"), "preserve conflict\n")
		before := readFile(t, filepath.Join(path, "source.txt"))
		value, err := snapshot.Record([]snapshot.Resource{{
			Identity: "phosphorco/workbench-fixture-library", Shape: contract.ResourceShape{Kind: contract.RepositoryShape},
			GitHub: "phosphorco/workbench-fixture-library", CanonicalPath: "repos/workbench-fixture-library", Commit: snapshotPruneV1Commit,
		}})
		if err != nil {
			t.Fatal(err)
		}
		observer := snapshotV1Observer{checkout: snapshot.Checkout{
			Exists: true, GitHub: "someone/else", Identity: "someone/else", Commit: snapshotPruneV1Commit, Clean: true,
		}}
		if _, err := snapshot.Plan(value, observer); err == nil {
			t.Fatal("conflicting exact-revision destination received acquisition authority")
		}
		if after := readFile(t, filepath.Join(path, "source.txt")); after != before {
			t.Fatalf("snapshot conflict changed source: before %q after %q", before, after)
		}
	})

	t.Run("dirty prune refusal then safe explicit removal", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "repos", "workbench-fixture-library")
		writeFile(t, filepath.Join(path, "source.txt"), "independently recoverable\n")
		candidate := orphan.Candidate{
			Identity: "phosphorco/workbench-fixture-library", GitHub: "phosphorco/workbench-fixture-library",
			Shape: contract.ResourceShape{Kind: contract.RepositoryShape}, CanonicalPath: "repos/workbench-fixture-library", Path: path,
		}
		safe := orphan.Observation{
			Exists: true, OriginCount: 1, OriginGitHub: candidate.GitHub, Branch: "main", Head: snapshotPruneV1Commit,
			UpstreamBranch: "origin/main", UpstreamHead: snapshotPruneV1Commit, RemoteHead: snapshotPruneV1Commit, Disposable: true,
		}
		dirty := safe
		dirty.Status = " M source.txt"
		removeCalls := 0
		if _, err := orphan.Preflight(orphan.Request{Root: root, Candidates: []orphan.Candidate{candidate}}, func(orphan.Candidate) (orphan.Observation, error) { return dirty, nil }); err == nil {
			t.Fatal("dirty orphan received prune authority")
		}
		if removeCalls != 0 {
			t.Fatalf("dirty refusal called remover %d times", removeCalls)
		}
		if got := readFile(t, filepath.Join(path, "source.txt")); got != "independently recoverable\n" {
			t.Fatalf("dirty refusal changed checkout: %q", got)
		}

		present := true
		observe := func(orphan.Candidate) (orphan.Observation, error) {
			if !present {
				return orphan.Observation{}, nil
			}
			return safe, nil
		}
		plan, err := orphan.Preflight(orphan.Request{Root: root, Candidates: []orphan.Candidate{candidate}}, observe)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := orphan.Apply(plan, observe, func(selected string) error {
			removeCalls++
			if selected != path {
				t.Fatalf("remove path = %q, want %q", selected, path)
			}
			if err := os.RemoveAll(selected); err != nil {
				return err
			}
			present = false
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		if removeCalls != 1 || len(receipt.RemovedPaths) != 1 {
			t.Fatalf("prune receipt = %#v, remove calls = %d", receipt, removeCalls)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("safe explicit prune left checkout: %v", err)
		}
	})
}

type snapshotV1Observer struct{ checkout snapshot.Checkout }

func (observer snapshotV1Observer) Observe(string) (snapshot.Checkout, error) {
	return observer.checkout, nil
}
