package acceptance

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/change"
)

// Local bare remotes exercise the recoverable saga mechanics only. Final V1
// acceptance must repeat this failure and recovery across the public fixtures.
func TestCrossRepositoryDeliveryV1RecoversPartialPushWithoutTouchingUnrelatedWork(t *testing.T) {
	ctx := context.Background()
	entry := newDeliveryV1Repository(t, "entry")
	library := newDeliveryV1Repository(t, "library")
	writeFile(t, filepath.Join(entry.work, "selected.txt"), "entry selected\n")
	writeFile(t, filepath.Join(entry.work, "unrelated.txt"), "entry unrelated remains dirty\n")
	writeFile(t, filepath.Join(library.work, "selected.txt"), "library selected\n")
	library.rejectPushes(t)

	requests := []change.Request{
		entry.request("readme-v1-change"),
		library.request("readme-v1-change"),
	}
	candidates, err := change.PrepareAll(ctx, requests)
	if err != nil {
		t.Fatal(err)
	}
	journal := filepath.Join(t.TempDir(), "change.jsonl")
	saga, err := change.Begin(journal, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if err := saga.AdvanceLocal(ctx); err != nil {
		t.Fatal(err)
	}
	if err := saga.Push(ctx); err == nil {
		t.Fatal("intentional second-remote rejection did not produce partial progress")
	}
	progress, err := saga.Progress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	assertDeliveryV1Progress(t, progress, "entry", true, true)
	assertDeliveryV1Progress(t, progress, "library", true, false)
	if status := git(t, entry.work, "status", "--short"); status != " M unrelated.txt\n" {
		t.Fatalf("unselected work changed: %q", status)
	}

	library.allowPushes(t)
	recovered, err := change.RecoverExact(journal, []change.Request{requests[1], requests[0]})
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Push(ctx); err != nil {
		t.Fatal(err)
	}
	completed, err := recovered.Progress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !completed.Complete {
		t.Fatalf("recovered saga incomplete: %+v", completed)
	}
	for index, repository := range []*deliveryV1Repository{entry, library} {
		message := git(t, repository.work, "show", "-s", "--format=%B", candidates[index].Commit)
		if strings.Count(message, "Workbench-Change-Id: readme-v1-change") != 1 {
			t.Fatalf("%s commit lacks one durable change identifier: %q", repository.id, message)
		}
		if count := strings.TrimSpace(git(t, repository.work, "rev-list", "--count", repository.initial+"..HEAD")); count != "1" {
			t.Fatalf("%s recovery duplicated commit: %s", repository.id, count)
		}
	}
}

type deliveryV1Repository struct {
	id      string
	work    string
	remote  string
	initial string
}

func newDeliveryV1Repository(t *testing.T, id string) *deliveryV1Repository {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	git(t, root, "init", "--bare", remote)
	git(t, root, "init", "-b", "main", work)
	git(t, work, "config", "user.name", "Workbench V1 Acceptance")
	git(t, work, "config", "user.email", "workbench-v1@example.invalid")
	writeFile(t, filepath.Join(work, "selected.txt"), "selected baseline\n")
	writeFile(t, filepath.Join(work, "unrelated.txt"), "unrelated baseline\n")
	git(t, work, "add", ".")
	git(t, work, "commit", "-m", "initial")
	git(t, work, "remote", "add", "origin", remote)
	git(t, work, "push", "-u", "origin", "main")
	return &deliveryV1Repository{id: id, work: work, remote: remote, initial: strings.TrimSpace(git(t, work, "rev-parse", "HEAD"))}
}

func (repository *deliveryV1Repository) request(changeID string) change.Request {
	return change.Request{
		ResourceID: repository.id, Repository: repository.work, Branch: "main", Remote: "origin",
		ChangeID: changeID, Title: "test: deliver " + repository.id,
		Description: "Deliver the exact selected Workbench V1 change.", Paths: []string{"selected.txt"},
	}
}

func (repository *deliveryV1Repository) rejectPushes(t *testing.T) {
	t.Helper()
	path := filepath.Join(repository.remote, "hooks", "pre-receive")
	writeFile(t, path, "#!/bin/sh\nexit 1\n")
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func (repository *deliveryV1Repository) allowPushes(t *testing.T) {
	t.Helper()
	path := filepath.Join(repository.remote, "hooks", "pre-receive")
	writeFile(t, path, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertDeliveryV1Progress(t *testing.T, progress change.Progress, id string, local, pushed bool) {
	t.Helper()
	for _, resource := range progress.Resources {
		if resource.ResourceID == id {
			if resource.Local != local || resource.Pushed != pushed {
				t.Fatalf("%s progress = %+v", id, resource)
			}
			return
		}
	}
	t.Fatalf("missing progress for %s", id)
}
