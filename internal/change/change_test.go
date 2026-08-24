package change_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/change"
)

func TestPrepareAllRefusesEveryRepositoryBeforeAdvancingRefs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first := newRepository(t, "first")
	second := newRepository(t, "second")
	first.write("selected.txt", "selected first\n")
	second.write("selected.txt", "selected second\n")
	second.hook("pre-commit", "#!/bin/sh\necho intentional-refusal >&2\nexit 41\n")

	_, err := change.PrepareAll(ctx, []change.Request{
		first.request("change-hooks", "selected.txt"),
		second.request("change-hooks", "selected.txt"),
	})
	if err == nil || !strings.Contains(err.Error(), "pre-commit") {
		t.Fatalf("expected pre-commit refusal, got %v", err)
	}
	first.assertHEAD(t, first.initialHEAD)
	second.assertHEAD(t, second.initialHEAD)
	first.assertIndexClean(t)
	second.assertIndexClean(t)
	if unreachable := first.git("fsck", "--unreachable", "--no-reflogs"); strings.Contains(unreachable, "unreachable commit") {
		t.Fatalf("later hook refusal left an earlier commit object:\n%s", unreachable)
	}
}

func TestPrepareAllRefusesHookWideningGeneratedPathsAndAmbiguousIndex(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("hook widening", func(t *testing.T) {
		repo := newRepository(t, "hook-widening")
		repo.write("selected.txt", "selected change\n")
		repo.write("unselected.txt", "unselected change\n")
		repo.hook("pre-commit", "#!/bin/sh\ngit add unselected.txt\n")
		_, err := change.PrepareAll(ctx, []change.Request{repo.request("change-widening", "selected.txt")})
		if err == nil || !strings.Contains(err.Error(), "widen") {
			t.Fatalf("expected widening refusal, got %v", err)
		}
		repo.assertHEAD(t, repo.initialHEAD)
		repo.assertIndexClean(t)
	})

	t.Run("hook filesystem edits are isolated", func(t *testing.T) {
		repo := newRepository(t, "hook-isolation")
		repo.write("selected.txt", "selected change\n")
		before := repo.read("unselected.txt")
		repo.hook("pre-commit", "#!/bin/sh\nprintf 'hook cwd must stay isolated\\n' > unselected.txt\nprintf 'hook script path must stay isolated\\n' > \"$(dirname \"$0\")/../../unselected.txt\"\n")
		if _, err := change.PrepareAll(ctx, []change.Request{repo.request("change-isolated-hook", "selected.txt")}); err != nil {
			t.Fatalf("isolated hook preparation: %v", err)
		}
		if after := repo.read("unselected.txt"); after != before {
			t.Fatalf("hook mutated real unrelated work: before %q after %q", before, after)
		}
		repo.assertHEAD(t, repo.initialHEAD)
		repo.assertIndexClean(t)
	})

	t.Run("generated path", func(t *testing.T) {
		repo := newRepository(t, "generated")
		repo.write("generated.json", "generated change\n")
		request := repo.request("change-generated", "generated.json")
		request.GeneratedPathPolicyID = "fixture-generated-v1"
		request.RejectPath = func(path string) bool { return path == "generated.json" }
		_, err := change.PrepareAll(ctx, []change.Request{request})
		if err == nil || !strings.Contains(err.Error(), "generated.json") {
			t.Fatalf("expected generated-path refusal, got %v", err)
		}
		repo.assertHEAD(t, repo.initialHEAD)
	})

	t.Run("pre-staged index", func(t *testing.T) {
		repo := newRepository(t, "staged")
		repo.write("unselected.txt", "pre-staged\n")
		repo.git("add", "unselected.txt")
		before := repo.git("diff", "--cached", "--binary")
		repo.write("selected.txt", "selected change\n")
		_, err := change.PrepareAll(ctx, []change.Request{repo.request("change-staged", "selected.txt")})
		if !errors.Is(err, change.ErrAmbiguousIndex) {
			t.Fatalf("expected ErrAmbiguousIndex, got %v", err)
		}
		if after := repo.git("diff", "--cached", "--binary"); after != before {
			t.Fatalf("index changed on refusal\nbefore:\n%s\nafter:\n%s", before, after)
		}
		repo.assertHEAD(t, repo.initialHEAD)
	})

	t.Run("shared trailer removed by hook", func(t *testing.T) {
		repo := newRepository(t, "message-hook")
		repo.write("selected.txt", "selected change\n")
		repo.hook("commit-msg", "#!/bin/sh\ngrep -v '^Workbench-Change-Id:' \"$1\" > \"$1.next\"\nmv \"$1.next\" \"$1\"\n")
		_, err := change.PrepareAll(ctx, []change.Request{repo.request("change-message", "selected.txt")})
		if err == nil || !strings.Contains(err.Error(), "preserve exactly one") {
			t.Fatalf("expected exact-trailer refusal, got %v", err)
		}
		repo.assertHEAD(t, repo.initialHEAD)
		repo.assertIndexClean(t)
	})
}

func TestLaterCommitTreeFailureLeavesNoBranchVisibleCommit(t *testing.T) {
	t.Parallel()
	first := newRepository(t, "materialized-first")
	second := newRepository(t, "materialization-fails")
	first.write("selected.txt", "first candidate\n")
	second.write("selected.txt", "second candidate\n")
	second.git("config", "user.name", "")
	second.git("config", "user.email", "")

	_, err := change.PrepareAll(context.Background(), []change.Request{
		first.request("materialization-failure", "selected.txt"),
		second.request("materialization-failure", "selected.txt"),
	})
	if err == nil || !strings.Contains(err.Error(), "materialize prepared commit") {
		t.Fatalf("later commit-tree failure = %v", err)
	}
	first.assertHEAD(t, first.initialHEAD)
	second.assertHEAD(t, second.initialHEAD)
	if unreachable := first.git("fsck", "--unreachable", "--no-reflogs"); !strings.Contains(unreachable, "unreachable commit") {
		t.Fatalf("test did not expose truthfully bounded dangling object:\n%s", unreachable)
	}
}

func TestFailingHookCannotMutateRealGitDatabase(t *testing.T) {
	t.Parallel()
	repo := newRepository(t, "hostile-hook")
	repo.write("selected.txt", "candidate\n")
	beforeGit := repo.identity()
	beforeObjects := repo.objectIdentity()
	beforeContent := repo.read("unselected.txt")
	repo.hook("pre-commit", `#!/bin/sh
tree=$(git write-tree) || exit 30
commit=$(printf 'hostile hook commit\n' | GIT_AUTHOR_NAME=Hook GIT_AUTHOR_EMAIL=hook@example.invalid GIT_COMMITTER_NAME=Hook GIT_COMMITTER_EMAIL=hook@example.invalid git commit-tree "$tree" -p HEAD) || exit 31
git update-ref HEAD "$commit" || exit 32
printf 'hostile loose object\n' | git hash-object -w --stdin >/dev/null || exit 33
exit 41
`)

	_, err := change.PrepareAll(context.Background(), []change.Request{repo.request("hostile-hook", "selected.txt")})
	if err == nil || !strings.Contains(err.Error(), "pre-commit hook failed") {
		t.Fatalf("hostile hook refusal = %v", err)
	}
	if after := repo.identity(); after != beforeGit {
		t.Fatalf("failing hook mutated real refs/index/status/content\nbefore:\n%s\nafter:\n%s", beforeGit, after)
	}
	if after := repo.objectIdentity(); after != beforeObjects {
		t.Fatalf("failing hook mutated real object set\nbefore:\n%s\nafter:\n%s", beforeObjects, after)
	}
	if after := repo.read("unselected.txt"); after != beforeContent {
		t.Fatalf("failing hook mutated unrelated content: before %q after %q", beforeContent, after)
	}
}

func TestPrepareAllRefusesStaleHunksAndUnacknowledgedDeletions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("stale hunk", func(t *testing.T) {
		repo := newRepository(t, "stale-hunk")
		repo.write("selected.txt", "first candidate\n")
		hunks, err := change.ListHunks(ctx, repo.root, "selected.txt")
		if err != nil || len(hunks) != 1 {
			t.Fatalf("list hunks: %v (%d hunks)", err, len(hunks))
		}
		repo.write("selected.txt", "different candidate\n")
		request := repo.request("change-stale")
		request.HunkIDs = []string{hunks[0].ID}
		_, err = change.PrepareAll(ctx, []change.Request{request})
		if !errors.Is(err, change.ErrStaleHunk) {
			t.Fatalf("expected ErrStaleHunk, got %v", err)
		}
		repo.assertHEAD(t, repo.initialHEAD)
		repo.assertIndexClean(t)
	})

	t.Run("unacknowledged deletion", func(t *testing.T) {
		repo := newRepository(t, "deletion")
		repo.remove("unselected.txt")
		repo.write("selected.txt", "selected change\n")
		_, err := change.PrepareAll(ctx, []change.Request{repo.request("change-deletion", "selected.txt")})
		if !errors.Is(err, change.ErrUnacknowledgedDeletion) {
			t.Fatalf("expected ErrUnacknowledgedDeletion, got %v", err)
		}
		repo.assertHEAD(t, repo.initialHEAD)
		repo.assertIndexClean(t)
	})
}

func TestExactHunkCommitPreservesAnotherHunkInTheSameFile(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newRepository(t, "hunks")
	repo.write("long.txt", strings.Join([]string{
		"line01", "line02", "line03", "line04", "line05", "line06", "line07", "line08", "line09", "line10",
		"line11", "line12", "line13", "line14", "line15", "line16", "line17", "line18", "line19", "line20", "",
	}, "\n"))
	repo.git("add", "long.txt")
	repo.git("commit", "-m", "add hunk baseline")
	repo.git("push", "origin", "main")
	repo.initialHEAD = strings.TrimSpace(repo.git("rev-parse", "HEAD"))
	repo.write("long.txt", strings.Join([]string{
		"line01", "line02 selected", "line03", "line04", "line05", "line06", "line07", "line08", "line09", "line10",
		"line11", "line12", "line13", "line14", "line15", "line16", "line17", "line18 unrelated", "line19", "line20", "",
	}, "\n"))
	hunks, err := change.ListHunks(ctx, repo.root, "long.txt")
	if err != nil || len(hunks) != 2 {
		t.Fatalf("list exact hunks: %v (%d hunks)", err, len(hunks))
	}
	var selected string
	for _, hunk := range hunks {
		if strings.Contains(hunk.Diff, "line02 selected") {
			selected = hunk.ID
		}
	}
	if selected == "" {
		t.Fatal("did not expose the selected hunk")
	}
	request := repo.request("exact-hunk")
	request.HunkIDs = []string{selected}
	candidates, err := change.PrepareAll(ctx, []change.Request{request})
	if err != nil {
		t.Fatalf("prepare exact hunk: %v", err)
	}
	saga, err := change.Begin(filepath.Join(t.TempDir(), "change.jsonl"), candidates)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := saga.AdvanceLocal(ctx); err != nil {
		t.Fatalf("advance: %v", err)
	}
	committed := repo.git("show", "HEAD:long.txt")
	if !strings.Contains(committed, "line02 selected") || strings.Contains(committed, "line18 unrelated") {
		t.Fatalf("commit did not contain only selected hunk:\n%s", committed)
	}
	remaining := repo.git("diff", "--", "long.txt")
	if strings.Contains(remaining, "line02 selected") || !strings.Contains(remaining, "line18 unrelated") {
		t.Fatalf("remaining work is not the unselected hunk:\n%s", remaining)
	}
}

func TestSagaPreservesExactWorkAndRecoversPartialPushAndLostAcknowledgement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first := newRepository(t, "entry")
	second := newRepository(t, "library")
	first.write("selected.txt", "entry selected\n")
	first.write("unselected.txt", "entry remains dirty\n")
	second.write("selected.txt", "library selected\n")
	second.rejectPushes("intentional partial push")

	requests := []change.Request{
		first.request("durable-change", "selected.txt"),
		second.request("durable-change", "selected.txt"),
	}
	candidates, err := change.PrepareAll(ctx, requests)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	journal := filepath.Join(t.TempDir(), "change.jsonl")
	saga, err := change.Begin(journal, candidates)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := saga.AdvanceLocal(ctx); err != nil {
		t.Fatalf("advance local: %v", err)
	}
	first.assertCommit(t, candidates[0].Commit, "durable-change", []string{"selected.txt"})
	second.assertCommit(t, candidates[1].Commit, "durable-change", []string{"selected.txt"})
	if status := first.git("status", "--short"); status != " M unselected.txt\n" {
		t.Fatalf("unselected work was not preserved: %q", status)
	}

	// This is the crash/lost-ack boundary: the remote already accepted the exact
	// commit, but the saga has not recorded a push observation.
	first.git("push", "origin", candidates[0].Commit+":refs/heads/main")
	if err := saga.Push(ctx); err == nil {
		t.Fatal("expected the library push to fail after entry was observed remotely")
	}
	progress, err := saga.Progress(ctx)
	if err != nil {
		t.Fatalf("observe partial progress: %v", err)
	}
	assertProgress(t, progress, "entry", true, true)
	assertProgress(t, progress, "library", true, false)

	second.allowPushes()
	recovered, err := change.RecoverExact(journal, []change.Request{requests[1], requests[0]})
	if err != nil {
		t.Fatalf("recover: %v", err)
	}
	if err := recovered.Push(ctx); err != nil {
		t.Fatalf("resume push: %v", err)
	}
	recoveredProgress, err := recovered.Progress(ctx)
	if err != nil {
		t.Fatalf("observe recovered progress: %v", err)
	}
	if !recoveredProgress.Complete {
		t.Fatal("recovered saga is not complete")
	}
	first.assertRemoteHEAD(t, candidates[0].Commit)
	second.assertRemoteHEAD(t, candidates[1].Commit)
	if count := strings.TrimSpace(first.git("rev-list", "--count", first.initialHEAD+"..HEAD")); count != "1" {
		t.Fatalf("successful commit was duplicated: %s commits", count)
	}
	completed, err := change.RecoverExact(journal, requests)
	if err != nil {
		t.Fatalf("recover completed exact plan: %v", err)
	}
	completedProgress, err := completed.Progress(ctx)
	if err != nil {
		t.Fatalf("observe completed progress: %v", err)
	}
	if !completedProgress.Complete {
		t.Fatalf("recovered exact plan is not complete: %+v", completedProgress)
	}
}

func TestSagaRecoversRefAdvanceBeforeJournalAcknowledgement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newRepository(t, "local-crash")
	repo.write("selected.txt", "selected after crash\n")
	candidates, err := change.PrepareAll(ctx, []change.Request{repo.request("local-crash", "selected.txt")})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	saga, err := change.Begin(filepath.Join(t.TempDir(), "change.jsonl"), candidates)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	repo.git("update-ref", "refs/heads/main", candidates[0].Commit, candidates[0].StartHEAD)
	if err := saga.AdvanceLocal(ctx); err != nil {
		t.Fatalf("recover local advance: %v", err)
	}
	repo.assertHEAD(t, candidates[0].Commit)
	repo.assertIndexClean(t)
}

func TestRecoverExactRefusesAlteredMissingExtraAndDuplicatePlanWithoutGitMutation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	first := newRepository(t, "recover-entry")
	second := newRepository(t, "recover-library")
	first.write("selected.txt", "entry candidate\n")
	second.write("selected.txt", "library candidate\n")
	requests := []change.Request{
		first.request("same-change-id", "selected.txt"),
		second.request("same-change-id", "selected.txt"),
	}
	for index := range requests {
		requests[index].GeneratedPathPolicyID = "world-generated-paths-v1"
		requests[index].RejectPath = func(path string) bool { return path == "generated.json" }
	}
	candidates, err := change.PrepareAll(ctx, requests)
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	journal := filepath.Join(t.TempDir(), "change.jsonl")
	if _, err := change.Begin(journal, candidates); err != nil {
		t.Fatalf("begin: %v", err)
	}
	firstBefore, secondBefore := first.identity(), second.identity()

	altered := append([]change.Request(nil), requests...)
	altered[0] = first.request("same-change-id", "unselected.txt")
	altered[0].GeneratedPathPolicyID = "world-generated-paths-v1"
	altered[0].RejectPath = requests[0].RejectPath
	alteredPolicy := append([]change.Request(nil), requests...)
	alteredPolicy[0].GeneratedPathPolicyID = "world-generated-paths-v2"
	extra := append(append([]change.Request(nil), requests...), first.request("same-change-id", "selected.txt"))
	cases := []struct {
		name string
		plan []change.Request
	}{
		{"altered selection", altered},
		{"altered generated-path policy", alteredPolicy},
		{"missing repository", requests[:1]},
		{"extra repository", extra},
		{"duplicate resource", []change.Request{requests[0], requests[0]}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := change.RecoverExact(journal, test.plan); err == nil {
				t.Fatal("expected exact-plan recovery refusal")
			}
			if after := first.identity(); after != firstBefore {
				t.Fatalf("first repository mutated\nbefore:\n%s\nafter:\n%s", firstBefore, after)
			}
			if after := second.identity(); after != secondBefore {
				t.Fatalf("second repository mutated\nbefore:\n%s\nafter:\n%s", secondBefore, after)
			}
		})
	}
}

func TestRecoverRefusesForgedOutOfOrderAndTruncatedJournalWithoutGitMutation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		event func(change.Candidate) string
	}{
		{"local advanced without intent", func(candidate change.Candidate) string {
			return fmt.Sprintf(`{"kind":"local-advanced","resourceId":%q,"commit":%q}`+"\n", candidate.ResourceID, candidate.Commit)
		}},
		{"push observed without intent", func(candidate change.Candidate) string {
			return fmt.Sprintf(`{"kind":"push-observed","resourceId":%q,"commit":%q}`+"\n", candidate.ResourceID, candidate.Commit)
		}},
		{"forged ordered local advancement", func(candidate change.Candidate) string {
			return fmt.Sprintf(`{"kind":"local-intent","resourceId":%q,"commit":%q}`+"\n"+`{"kind":"local-advanced","resourceId":%q,"commit":%q}`+"\n", candidate.ResourceID, candidate.Commit, candidate.ResourceID, candidate.Commit)
		}},
		{"completed before progress", func(change.Candidate) string { return `{"kind":"completed"}` + "\n" }},
		{"truncated event", func(candidate change.Candidate) string {
			return fmt.Sprintf(`{"kind":"local-intent","resourceId":%q`, candidate.ResourceID)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := newRepository(t, "journal-refusal")
			repo.write("selected.txt", "candidate\n")
			candidates, err := change.PrepareAll(context.Background(), []change.Request{repo.request("journal-refusal", "selected.txt")})
			if err != nil {
				t.Fatal(err)
			}
			journal := filepath.Join(t.TempDir(), "change.jsonl")
			if _, err := change.Begin(journal, candidates); err != nil {
				t.Fatal(err)
			}
			before := repo.identity()
			appendJournal(t, journal, test.event(candidates[0]))
			if _, err := change.Recover(journal); err == nil {
				t.Fatal("unsafe journal recovered")
			}
			if after := repo.identity(); after != before {
				t.Fatalf("journal refusal mutated Git\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}
}

func TestProgressAndRecoveryReobserveDurableRemotePush(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := newRepository(t, "remote-reobservation")
	repo.write("selected.txt", "candidate\n")
	request := repo.request("remote-reobservation", "selected.txt")
	candidates, err := change.PrepareAll(ctx, []change.Request{request})
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
	if err := saga.Push(ctx); err != nil {
		t.Fatal(err)
	}
	if progress, err := saga.Progress(ctx); err != nil || !progress.Complete {
		t.Fatalf("initial exact progress = %+v, %v", progress, err)
	}

	// Simulate an independently changed/deleted public branch after the durable
	// observation. Historical journal text must not masquerade as current proof.
	repo.gitAt(repo.remote, "update-ref", "refs/heads/main", repo.initialHEAD, candidates[0].Commit)
	if _, err := saga.Progress(ctx); err == nil {
		t.Fatal("final progress trusted a moved remote branch")
	}
	if _, err := change.RecoverExact(journal, []change.Request{request}); err == nil {
		t.Fatal("recovery trusted a moved remote branch")
	}
}

func assertProgress(t *testing.T, progress change.Progress, resource string, local, pushed bool) {
	t.Helper()
	for _, state := range progress.Resources {
		if state.ResourceID == resource {
			if state.Local != local || state.Pushed != pushed {
				t.Fatalf("unexpected %s progress: %+v", resource, state)
			}
			return
		}
	}
	t.Fatalf("missing progress for %s", resource)
}

type repository struct {
	t           *testing.T
	root        string
	remote      string
	resource    string
	initialHEAD string
}

func newRepository(t *testing.T, resource string) *repository {
	t.Helper()
	root := t.TempDir()
	remote := filepath.Join(root, "remote.git")
	work := filepath.Join(root, "work")
	run(t, root, "git", "init", "--bare", remote)
	run(t, root, "git", "init", "-b", "main", work)
	run(t, work, "git", "config", "user.name", "Workbench Test")
	run(t, work, "git", "config", "user.email", "workbench@example.invalid")
	write(t, filepath.Join(work, "selected.txt"), "selected baseline\n")
	write(t, filepath.Join(work, "unselected.txt"), "unselected baseline\n")
	run(t, work, "git", "add", ".")
	run(t, work, "git", "commit", "-m", "initial")
	run(t, work, "git", "remote", "add", "origin", remote)
	run(t, work, "git", "push", "-u", "origin", "main")
	return &repository{t: t, root: work, remote: remote, resource: resource, initialHEAD: strings.TrimSpace(run(t, work, "git", "rev-parse", "HEAD"))}
}

func (repo *repository) request(changeID string, paths ...string) change.Request {
	return change.Request{
		ResourceID:  repo.resource,
		Repository:  repo.root,
		Branch:      "main",
		Remote:      "origin",
		ChangeID:    changeID,
		Title:       "test: deliver " + repo.resource,
		Description: "Deliver only the exact selected work.",
		Paths:       paths,
	}
}

func (repo *repository) read(path string) string {
	repo.t.Helper()
	encoded, err := os.ReadFile(filepath.Join(repo.root, path))
	if err != nil {
		repo.t.Fatal(err)
	}
	return string(encoded)
}

func (repo *repository) gitAt(directory string, arguments ...string) string {
	repo.t.Helper()
	return strings.TrimSpace(run(repo.t, directory, "git", arguments...))
}

func appendJournal(t *testing.T, path, contents string) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(contents); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func (repo *repository) write(path, content string) {
	repo.t.Helper()
	write(repo.t, filepath.Join(repo.root, path), content)
}
func (repo *repository) remove(path string) {
	repo.t.Helper()
	if err := os.Remove(filepath.Join(repo.root, path)); err != nil {
		repo.t.Fatal(err)
	}
}
func (repo *repository) git(arguments ...string) string {
	repo.t.Helper()
	return run(repo.t, repo.root, "git", arguments...)
}
func (repo *repository) hook(name, content string) {
	repo.t.Helper()
	path := strings.TrimSpace(repo.git("rev-parse", "--path-format=absolute", "--git-path", "hooks/"+name))
	write(repo.t, path, content)
	if err := os.Chmod(path, 0o755); err != nil {
		repo.t.Fatal(err)
	}
}
func (repo *repository) rejectPushes(message string) {
	repo.t.Helper()
	path := filepath.Join(repo.remote, "hooks", "pre-receive")
	write(repo.t, path, "#!/bin/sh\necho "+message+" >&2\nexit 1\n")
	if err := os.Chmod(path, 0o755); err != nil {
		repo.t.Fatal(err)
	}
}
func (repo *repository) allowPushes() {
	repo.t.Helper()
	repo.rejectPushes("allowed")
	write(repo.t, filepath.Join(repo.remote, "hooks", "pre-receive"), "#!/bin/sh\nexit 0\n")
}
func (repo *repository) assertHEAD(t *testing.T, expected string) {
	t.Helper()
	if actual := strings.TrimSpace(repo.git("rev-parse", "HEAD")); actual != expected {
		t.Fatalf("HEAD = %s, want %s", actual, expected)
	}
}
func (repo *repository) assertIndexClean(t *testing.T) {
	t.Helper()
	if diff := repo.git("diff", "--cached", "--binary"); diff != "" {
		t.Fatalf("index is dirty:\n%s", diff)
	}
}
func (repo *repository) assertCommit(t *testing.T, commit, changeID string, paths []string) {
	t.Helper()
	repo.assertHEAD(t, commit)
	message := repo.git("show", "-s", "--format=%B", commit)
	trailer := "Workbench-Change-Id: " + changeID
	if strings.Count(message, trailer) != 1 {
		t.Fatalf("missing exact trailer in %q", message)
	}
	actual := strings.Fields(repo.git("show", "--format=", "--name-only", commit))
	if strings.Join(actual, "\n") != strings.Join(paths, "\n") {
		t.Fatalf("committed paths = %v, want %v", actual, paths)
	}
}
func (repo *repository) assertRemoteHEAD(t *testing.T, expected string) {
	t.Helper()
	fields := strings.Fields(repo.git("ls-remote", "--heads", "origin", "refs/heads/main"))
	if len(fields) != 2 || fields[0] != expected {
		t.Fatalf("remote HEAD = %v, want %s", fields, expected)
	}
}

func (repo *repository) identity() string {
	repo.t.Helper()
	return strings.Join([]string{
		repo.git("rev-parse", "HEAD"),
		repo.git("show-ref", "--head"),
		repo.git("write-tree"),
		repo.git("status", "--porcelain=v2", "--untracked-files=all"),
		repo.git("diff", "--binary"),
		repo.git("diff", "--cached", "--binary"),
	}, "\x00")
}

func (repo *repository) objectIdentity() string {
	repo.t.Helper()
	objects := strings.Fields(repo.git("cat-file", "--batch-all-objects", "--batch-check=%(objectname)"))
	sort.Strings(objects)
	return strings.Join(objects, "\n")
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, directory, command string, arguments ...string) string {
	t.Helper()
	cmd := exec.Command(command, arguments...)
	cmd.Dir = directory
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %s: %v\n%s", command, strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
