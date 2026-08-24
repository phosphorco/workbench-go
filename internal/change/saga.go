package change

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

type ResourceProgress struct {
	ResourceID string
	Local      bool
	Pushed     bool
}

type Progress struct {
	Resources []ResourceProgress
	Complete  bool
}

type PartialPushError struct {
	ResourceID string
	Cause      error
}

func (failure *PartialPushError) Error() string {
	return fmt.Sprintf("push change set is partial at %s: %v", failure.ResourceID, failure.Cause)
}
func (failure *PartialPushError) Unwrap() error { return failure.Cause }

type Saga struct {
	journal     string
	candidates  []Candidate
	localIntent map[string]bool
	local       map[string]bool
	pushIntent  map[string]bool
	pushed      map[string]bool
	complete    bool
}

type journalEvent struct {
	Kind       string      `json:"kind"`
	ChangeID   string      `json:"changeId,omitempty"`
	Candidates []Candidate `json:"candidates,omitempty"`
	ResourceID string      `json:"resourceId,omitempty"`
	Commit     string      `json:"commit,omitempty"`
}

func Begin(journal string, candidates []Candidate) (*Saga, error) {
	if err := validateCandidates(candidates); err != nil {
		return nil, err
	}
	if info, err := os.Stat(journal); err == nil {
		if info.Size() == 0 {
			return nil, fmt.Errorf("journal %q exists but has no durable creation event", journal)
		}
		recovered, err := Recover(journal)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(recovered.candidates, candidates) {
			return nil, errors.New("existing journal belongs to a different prepared change set")
		}
		return recovered, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("observe change journal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(journal), 0o755); err != nil {
		return nil, fmt.Errorf("create change journal directory: %w", err)
	}
	file, err := os.OpenFile(journal, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create change journal: %w", err)
	}
	event := journalEvent{Kind: "created", ChangeID: candidates[0].ChangeID, Candidates: candidates}
	if err := writeEvent(file, event); err != nil {
		file.Close()
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, fmt.Errorf("close change journal: %w", err)
	}
	if err := syncDirectory(filepath.Dir(journal)); err != nil {
		return nil, err
	}
	return newSaga(journal, append([]Candidate(nil), candidates...)), nil
}

func Recover(journal string) (*Saga, error) {
	file, err := os.Open(journal)
	if err != nil {
		return nil, fmt.Errorf("open change journal: %w", err)
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	var events []journalEvent
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) != 0 && line[len(line)-1] == '\n' {
			var event journalEvent
			decoder := json.NewDecoder(bytes.NewReader(line))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&event); err != nil {
				return nil, fmt.Errorf("decode durable journal event: %w", err)
			}
			events = append(events, event)
		} else if len(line) != 0 {
			return nil, errors.New("change journal contains a non-durable partial event")
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("read change journal: %w", readErr)
		}
	}
	if len(events) == 0 || events[0].Kind != "created" {
		return nil, errors.New("change journal has no durable creation event")
	}
	if err := validateCandidates(events[0].Candidates); err != nil {
		return nil, err
	}
	if events[0].ChangeID != events[0].Candidates[0].ChangeID || events[0].ResourceID != "" || events[0].Commit != "" {
		return nil, errors.New("journal creation event does not exactly identify its prepared change set")
	}
	saga := newSaga(journal, events[0].Candidates)
	known := make(map[string]string, len(saga.candidates))
	for _, candidate := range saga.candidates {
		known[candidate.ResourceID] = candidate.Commit
	}
	for index, event := range events[1:] {
		if event.ChangeID != "" || len(event.Candidates) != 0 {
			return nil, fmt.Errorf("journal %s event carries fields outside its authority", event.Kind)
		}
		expected, exists := known[event.ResourceID]
		switch event.Kind {
		case "local-intent":
			if !exists || event.Commit != expected {
				return nil, errors.New("journal local intent does not name a prepared commit")
			}
			if saga.localIntent[event.ResourceID] || saga.local[event.ResourceID] || saga.pushIntent[event.ResourceID] || saga.pushed[event.ResourceID] {
				return nil, errors.New("journal local intent is out of order or duplicated")
			}
			saga.localIntent[event.ResourceID] = true
		case "local-advanced":
			if !exists || event.Commit != expected || !saga.localIntent[event.ResourceID] || saga.local[event.ResourceID] {
				return nil, errors.New("journal local advancement does not name a prepared commit")
			}
			saga.local[event.ResourceID] = true
		case "push-intent":
			if !exists || event.Commit != expected || !saga.local[event.ResourceID] || saga.pushIntent[event.ResourceID] || saga.pushed[event.ResourceID] {
				return nil, errors.New("journal push intent is out of order or duplicated")
			}
			saga.pushIntent[event.ResourceID] = true
		case "push-observed":
			if !exists || event.Commit != expected || !saga.pushIntent[event.ResourceID] || saga.pushed[event.ResourceID] {
				return nil, errors.New("journal push observation does not name a prepared commit")
			}
			saga.pushed[event.ResourceID] = true
		case "completed":
			if event.ResourceID != "" || event.Commit != "" || saga.complete || index != len(events[1:])-1 {
				return nil, errors.New("journal completion event is out of order or malformed")
			}
			for _, candidate := range saga.candidates {
				if !saga.local[candidate.ResourceID] || !saga.pushed[candidate.ResourceID] {
					return nil, errors.New("completed journal is missing repository progress")
				}
			}
			saga.complete = true
		default:
			return nil, fmt.Errorf("journal contains unknown event kind %q", event.Kind)
		}
	}
	if err := saga.verifyDurableLocals(context.Background()); err != nil {
		return nil, err
	}
	if err := saga.verifyDurablePushes(context.Background()); err != nil {
		return nil, err
	}
	return saga, nil
}

func newSaga(journal string, candidates []Candidate) *Saga {
	return &Saga{
		journal: journal, candidates: candidates,
		localIntent: make(map[string]bool), local: make(map[string]bool),
		pushIntent: make(map[string]bool), pushed: make(map[string]bool),
	}
}

// RecoverExact resumes a durable saga only when the newly evaluated plan is
// semantically identical to the plan that produced every stored candidate.
// Repository and selection order do not participate in identity.
func RecoverExact(journal string, requests []Request) (*Saga, error) {
	saga, err := Recover(journal)
	if err != nil {
		return nil, err
	}
	if len(requests) != len(saga.candidates) {
		return nil, fmt.Errorf("evaluated plan has %d repositories; durable change set has %d", len(requests), len(saga.candidates))
	}
	candidates := make(map[string]Candidate, len(saga.candidates))
	for _, candidate := range saga.candidates {
		candidates[candidate.ResourceID] = candidate
	}
	seen := make(map[string]struct{}, len(requests))
	for _, request := range requests {
		if err := validateRequest(request); err != nil {
			return nil, fmt.Errorf("validate recovery request for %q: %w", request.ResourceID, err)
		}
		if _, duplicate := seen[request.ResourceID]; duplicate {
			return nil, fmt.Errorf("evaluated plan repeats resource %q", request.ResourceID)
		}
		seen[request.ResourceID] = struct{}{}
		candidate, exists := candidates[request.ResourceID]
		if !exists {
			return nil, fmt.Errorf("evaluated plan contains unexpected resource %q", request.ResourceID)
		}
		root, err := repositoryRoot(context.Background(), request.Repository)
		if err != nil {
			return nil, err
		}
		digest, err := requestDigest(request, root)
		if err != nil {
			return nil, err
		}
		if digest != candidate.RequestDigest {
			return nil, fmt.Errorf("evaluated plan for resource %q differs from its durable exact candidate", request.ResourceID)
		}
		remoteURL, err := observeRemoteURL(context.Background(), root, request.Remote)
		if err != nil {
			return nil, err
		}
		if root != candidate.Repository || request.Branch != candidate.Branch || request.Remote != candidate.Remote || request.ChangeID != candidate.ChangeID || remoteURL != candidate.RemoteURL {
			return nil, fmt.Errorf("evaluated repository authority for %q differs from its durable exact candidate", request.ResourceID)
		}
	}
	for _, candidate := range saga.candidates {
		if _, exists := seen[candidate.ResourceID]; !exists {
			return nil, fmt.Errorf("evaluated plan omits durable resource %q", candidate.ResourceID)
		}
	}
	return saga, nil
}

func (saga *Saga) AdvanceLocal(ctx context.Context) error {
	for _, candidate := range saga.candidates {
		if saga.local[candidate.ResourceID] {
			continue
		}
		if !saga.localIntent[candidate.ResourceID] {
			if err := saga.append(journalEvent{Kind: "local-intent", ResourceID: candidate.ResourceID, Commit: candidate.Commit}); err != nil {
				return err
			}
			saga.localIntent[candidate.ResourceID] = true
		}
		head, err := gitText(ctx, candidate.Repository, nil, nil, "rev-parse", "HEAD")
		if err != nil {
			return err
		}
		branch, err := gitText(ctx, candidate.Repository, nil, nil, "branch", "--show-current")
		if err != nil {
			return err
		}
		if branch != candidate.Branch {
			return fmt.Errorf("%s is on branch %q, expected %q", candidate.ResourceID, branch, candidate.Branch)
		}
		indexTree, err := gitText(ctx, candidate.Repository, nil, nil, "write-tree")
		if err != nil {
			return err
		}
		switch head {
		case candidate.StartHEAD:
			if indexTree != candidate.StartTree {
				return fmt.Errorf("%s index changed after preparation", candidate.ResourceID)
			}
			status, err := git(ctx, candidate.Repository, nil, nil, "status", "--porcelain=v2", "-z", "--untracked-files=all")
			if err != nil {
				return err
			}
			if digestBytes(status) != candidate.StatusDigest {
				return fmt.Errorf("%s worktree changed after preparation", candidate.ResourceID)
			}
			if _, err := git(ctx, candidate.Repository, nil, nil, "update-ref", "refs/heads/"+candidate.Branch, candidate.Commit, candidate.StartHEAD); err != nil {
				return err
			}
		case candidate.Commit:
			if indexTree != candidate.StartTree && indexTree != candidate.Tree {
				return fmt.Errorf("%s index cannot be recovered after local ref advancement", candidate.ResourceID)
			}
		default:
			return fmt.Errorf("%s HEAD changed from prepared %s to %s", candidate.ResourceID, candidate.StartHEAD, head)
		}
		if indexTree != candidate.Tree {
			if _, err := git(ctx, candidate.Repository, nil, nil, "read-tree", candidate.Commit); err != nil {
				return fmt.Errorf("restore exact index after local advancement: %w", err)
			}
		}
		if err := saga.append(journalEvent{Kind: "local-advanced", ResourceID: candidate.ResourceID, Commit: candidate.Commit}); err != nil {
			return err
		}
		saga.local[candidate.ResourceID] = true
	}
	return nil
}

func (saga *Saga) Push(ctx context.Context) error {
	for _, candidate := range saga.candidates {
		if !saga.local[candidate.ResourceID] {
			return fmt.Errorf("%s has no durable local commit", candidate.ResourceID)
		}
	}
	for _, candidate := range saga.candidates {
		if saga.pushed[candidate.ResourceID] {
			continue
		}
		currentURL, err := observeRemoteURL(ctx, candidate.Repository, candidate.Remote)
		if err != nil {
			return &PartialPushError{ResourceID: candidate.ResourceID, Cause: err}
		}
		if currentURL != candidate.RemoteURL {
			return &PartialPushError{ResourceID: candidate.ResourceID, Cause: fmt.Errorf("remote push URL changed after preparation")}
		}
		if !saga.pushIntent[candidate.ResourceID] {
			if err := saga.append(journalEvent{Kind: "push-intent", ResourceID: candidate.ResourceID, Commit: candidate.Commit}); err != nil {
				return err
			}
			saga.pushIntent[candidate.ResourceID] = true
		}
		remote, err := remoteHEAD(ctx, candidate.Repository, candidate.Remote, candidate.Branch)
		if err != nil {
			return &PartialPushError{ResourceID: candidate.ResourceID, Cause: err}
		}
		if remote != candidate.Commit {
			if remote != candidate.StartRemote {
				return &PartialPushError{ResourceID: candidate.ResourceID, Cause: fmt.Errorf("remote branch changed from prepared %s to %s", candidate.StartRemote, remote)}
			}
			refspec := candidate.Commit + ":refs/heads/" + candidate.Branch
			if _, err := git(ctx, candidate.Repository, nil, nil, "push", candidate.Remote, refspec); err != nil {
				return &PartialPushError{ResourceID: candidate.ResourceID, Cause: err}
			}
			remote, err = remoteHEAD(ctx, candidate.Repository, candidate.Remote, candidate.Branch)
			if err != nil {
				return &PartialPushError{ResourceID: candidate.ResourceID, Cause: err}
			}
			if remote != candidate.Commit {
				return &PartialPushError{ResourceID: candidate.ResourceID, Cause: fmt.Errorf("remote did not expose pushed commit %s", candidate.Commit)}
			}
		}
		if err := saga.append(journalEvent{Kind: "push-observed", ResourceID: candidate.ResourceID, Commit: candidate.Commit}); err != nil {
			return err
		}
		saga.pushed[candidate.ResourceID] = true
	}
	if !saga.complete {
		if err := saga.append(journalEvent{Kind: "completed"}); err != nil {
			return err
		}
		saga.complete = true
	}
	return nil
}

func (saga *Saga) Progress(ctx context.Context) (Progress, error) {
	if err := saga.verifyDurableLocals(ctx); err != nil {
		return Progress{}, err
	}
	if err := saga.verifyDurablePushes(ctx); err != nil {
		return Progress{}, err
	}
	progress := Progress{Resources: make([]ResourceProgress, 0, len(saga.candidates)), Complete: saga.complete}
	for _, candidate := range saga.candidates {
		progress.Resources = append(progress.Resources, ResourceProgress{ResourceID: candidate.ResourceID, Local: saga.local[candidate.ResourceID], Pushed: saga.pushed[candidate.ResourceID]})
	}
	return progress, nil
}

func (saga *Saga) verifyDurableLocals(ctx context.Context) error {
	for _, candidate := range saga.candidates {
		if !saga.local[candidate.ResourceID] {
			continue
		}
		branch, err := gitText(ctx, candidate.Repository, nil, nil, "branch", "--show-current")
		if err != nil {
			return fmt.Errorf("re-observe durable local branch for %s: %w", candidate.ResourceID, err)
		}
		head, err := gitText(ctx, candidate.Repository, nil, nil, "rev-parse", "HEAD")
		if err != nil {
			return fmt.Errorf("re-observe durable local commit for %s: %w", candidate.ResourceID, err)
		}
		index, err := gitText(ctx, candidate.Repository, nil, nil, "write-tree")
		if err != nil {
			return fmt.Errorf("re-observe durable local index for %s: %w", candidate.ResourceID, err)
		}
		if branch != candidate.Branch || head != candidate.Commit || index != candidate.Tree {
			return fmt.Errorf("durable local advancement for %s is not present at the exact branch, commit, and index", candidate.ResourceID)
		}
	}
	return nil
}

func (saga *Saga) verifyDurablePushes(ctx context.Context) error {
	for _, candidate := range saga.candidates {
		if !saga.pushed[candidate.ResourceID] {
			continue
		}
		currentURL, err := observeRemoteURL(ctx, candidate.Repository, candidate.Remote)
		if err != nil {
			return fmt.Errorf("re-observe durable push URL for %s: %w", candidate.ResourceID, err)
		}
		if currentURL != candidate.RemoteURL {
			return fmt.Errorf("durable push remote for %s changed after observation", candidate.ResourceID)
		}
		remote, err := remoteHEAD(ctx, candidate.Repository, candidate.Remote, candidate.Branch)
		if err != nil {
			return fmt.Errorf("re-observe durable push for %s: %w", candidate.ResourceID, err)
		}
		if remote != candidate.Commit {
			return fmt.Errorf("durable push for %s is no longer present at the exact remote branch", candidate.ResourceID)
		}
	}
	return nil
}

func (saga *Saga) append(event journalEvent) error {
	file, err := os.OpenFile(saga.journal, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("open change journal for append: %w", err)
	}
	if err := writeEvent(file, event); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close change journal: %w", err)
	}
	return nil
}

func writeEvent(file *os.File, event journalEvent) error {
	encoded, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode change journal event: %w", err)
	}
	encoded = append(encoded, '\n')
	if n, err := file.Write(encoded); err != nil {
		return fmt.Errorf("append change journal event: %w", err)
	} else if n != len(encoded) {
		return io.ErrShortWrite
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync change journal event: %w", err)
	}
	return nil
}

func syncDirectory(directory string) error {
	file, err := os.Open(directory)
	if err != nil {
		return fmt.Errorf("open journal directory: %w", err)
	}
	defer file.Close()
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync journal directory: %w", err)
	}
	return nil
}

func validateCandidates(candidates []Candidate) error {
	if len(candidates) == 0 {
		return errors.New("prepared change set has no repositories")
	}
	changeID := candidates[0].ChangeID
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if candidate.ResourceID == "" || candidate.Repository == "" || candidate.Branch == "" || candidate.Remote == "" || candidate.RemoteURL == "" || candidate.Commit == "" || candidate.RequestDigest == "" || candidate.StatusDigest == "" {
			return errors.New("prepared candidate is incomplete")
		}
		if candidate.ChangeID != changeID {
			return errors.New("prepared candidates do not share one change identifier")
		}
		if _, duplicate := seen[candidate.ResourceID]; duplicate {
			return fmt.Errorf("prepared resource %q appears more than once", candidate.ResourceID)
		}
		seen[candidate.ResourceID] = struct{}{}
		root, err := repositoryRoot(context.Background(), candidate.Repository)
		if err != nil {
			return err
		}
		if root != candidate.Repository {
			return fmt.Errorf("prepared repository %q is not canonical", candidate.Repository)
		}
		parent, err := gitText(context.Background(), root, nil, nil, "show", "-s", "--format=%P", candidate.Commit)
		if err != nil {
			return fmt.Errorf("observe prepared commit: %w", err)
		}
		if parent != candidate.StartHEAD {
			return fmt.Errorf("prepared commit for %s does not have the observed parent", candidate.ResourceID)
		}
		tree, err := gitText(context.Background(), root, nil, nil, "show", "-s", "--format=%T", candidate.Commit)
		if err != nil {
			return err
		}
		if tree != candidate.Tree {
			return fmt.Errorf("prepared commit for %s does not have the approved tree", candidate.ResourceID)
		}
		message, err := git(context.Background(), root, nil, nil, "show", "-s", "--format=%B", candidate.Commit)
		if err != nil {
			return err
		}
		if strings.TrimRight(string(message), "\n") != strings.TrimRight(candidate.Message, "\n") {
			return fmt.Errorf("prepared commit for %s does not have the approved message", candidate.ResourceID)
		}
		if !hasExactChangeTrailer(candidate.Message, "Workbench-Change-Id: "+candidate.ChangeID) {
			return fmt.Errorf("prepared commit for %s does not have one exact shared change trailer", candidate.ResourceID)
		}
	}
	return nil
}
