package change

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

var (
	ErrAmbiguousIndex         = errors.New("repository index contains pre-staged changes")
	ErrStaleHunk              = errors.New("selected hunk is stale or absent")
	ErrUnacknowledgedDeletion = errors.New("tracked deletion is not selected or acknowledged")
)

type Request struct {
	ResourceID            string
	Repository            string
	Branch                string
	Remote                string
	ChangeID              string
	Title                 string
	Description           string
	Paths                 []string
	HunkIDs               []string
	UnrelatedDeletedPaths []string
	GeneratedPathPolicyID string
	RejectPath            func(string) bool
}

type Hunk struct {
	ID   string
	Path string
	Diff string
}

type Candidate struct {
	ResourceID    string `json:"resourceId"`
	Repository    string `json:"repository"`
	Branch        string `json:"branch"`
	Remote        string `json:"remote"`
	RemoteURL     string `json:"remoteUrl"`
	ChangeID      string `json:"changeId"`
	RequestDigest string `json:"requestDigest"`
	StatusDigest  string `json:"statusDigest"`
	StartHEAD     string `json:"startHead"`
	StartTree     string `json:"startTree"`
	StartRemote   string `json:"startRemote"`
	Tree          string `json:"tree"`
	Commit        string `json:"commit"`
	Message       string `json:"message"`
}

type parsedHunk struct {
	Hunk
	patch string
}

func ListHunks(ctx context.Context, repository string, paths ...string) ([]Hunk, error) {
	root, err := repositoryRoot(ctx, repository)
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		paths, err = changedPaths(ctx, root, nil, false)
		if err != nil {
			return nil, err
		}
	}
	var result []Hunk
	for _, path := range paths {
		if err := validatePath(path); err != nil {
			return nil, err
		}
		hunks, err := hunksForPath(ctx, root, path)
		if err != nil {
			return nil, err
		}
		for _, hunk := range hunks {
			result = append(result, hunk.Hunk)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Path == result[j].Path {
			return result[i].ID < result[j].ID
		}
		return result[i].Path < result[j].Path
	})
	return result, nil
}

// PrepareAll performs every repository observation, exact-index construction,
// and fallible commit hook before it creates any branch-visible commit.
func PrepareAll(ctx context.Context, requests []Request) ([]Candidate, error) {
	if len(requests) == 0 {
		return nil, errors.New("change set has no repositories")
	}
	seen := make(map[string]struct{}, len(requests))
	changeID := requests[0].ChangeID
	candidates := make([]Candidate, 0, len(requests))
	for _, request := range requests {
		if request.ChangeID != changeID {
			return nil, errors.New("all repository requests must share one change identifier")
		}
		if _, exists := seen[request.ResourceID]; exists {
			return nil, fmt.Errorf("resource %q appears more than once", request.ResourceID)
		}
		seen[request.ResourceID] = struct{}{}
		candidate, err := prepare(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("prepare %s: %w", request.ResourceID, err)
		}
		candidates = append(candidates, candidate)
	}
	// Commit objects are created only after every repository's fallible hook has
	// accepted its exact candidate. A hook refusal therefore creates no commit
	// object. A later commit-tree failure can leave an unreachable object in an
	// earlier repository, but no branch-visible commit; refs advance only in the
	// recoverable saga.
	for index := range candidates {
		commit, err := gitText(ctx, candidates[index].Repository, nil, []byte(candidates[index].Message), "commit-tree", candidates[index].Tree, "-p", candidates[index].StartHEAD)
		if err != nil {
			return nil, fmt.Errorf("materialize prepared commit for %s: %w", candidates[index].ResourceID, err)
		}
		candidates[index].Commit = commit
	}
	return candidates, nil
}

func prepare(ctx context.Context, request Request) (Candidate, error) {
	if err := validateRequest(request); err != nil {
		return Candidate{}, err
	}
	root, err := repositoryRoot(ctx, request.Repository)
	if err != nil {
		return Candidate{}, err
	}
	if _, err := git(ctx, root, nil, nil, "check-ref-format", "--branch", request.Branch); err != nil {
		return Candidate{}, fmt.Errorf("invalid subject branch %q: %w", request.Branch, err)
	}
	branch, err := gitText(ctx, root, nil, nil, "branch", "--show-current")
	if err != nil {
		return Candidate{}, err
	}
	if branch != request.Branch {
		return Candidate{}, fmt.Errorf("repository is on branch %q, expected %q", branch, request.Branch)
	}
	staged, err := changedPaths(ctx, root, nil, true)
	if err != nil {
		return Candidate{}, err
	}
	if len(staged) != 0 {
		return Candidate{}, fmt.Errorf("%w: %s", ErrAmbiguousIndex, strings.Join(staged, ", "))
	}
	startHEAD, err := gitText(ctx, root, nil, nil, "rev-parse", "HEAD")
	if err != nil {
		return Candidate{}, err
	}
	startTree, err := gitText(ctx, root, nil, nil, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return Candidate{}, err
	}
	indexTree, err := gitText(ctx, root, nil, nil, "write-tree")
	if err != nil {
		return Candidate{}, fmt.Errorf("%w: %v", ErrAmbiguousIndex, err)
	}
	if indexTree != startTree {
		return Candidate{}, fmt.Errorf("%w: index tree differs from HEAD", ErrAmbiguousIndex)
	}

	fullPaths := make(map[string]struct{}, len(request.Paths))
	for _, path := range request.Paths {
		if err := validatePath(path); err != nil {
			return Candidate{}, err
		}
		if request.RejectPath != nil && request.RejectPath(path) {
			return Candidate{}, fmt.Errorf("selected path %q is generated or Workbench-owned", path)
		}
		if _, duplicate := fullPaths[path]; duplicate {
			return Candidate{}, fmt.Errorf("selected path %q appears more than once", path)
		}
		fullPaths[path] = struct{}{}
	}

	allHunks, err := parseAllHunks(ctx, root)
	if err != nil {
		return Candidate{}, err
	}
	hunksByID := make(map[string]parsedHunk, len(allHunks))
	for _, hunk := range allHunks {
		if _, collision := hunksByID[hunk.ID]; collision {
			return Candidate{}, fmt.Errorf("hunk identifier %q is ambiguous", hunk.ID)
		}
		hunksByID[hunk.ID] = hunk
	}
	selectedHunks := make([]parsedHunk, 0, len(request.HunkIDs))
	hunkPaths := make(map[string]struct{})
	selectedIDs := make(map[string]struct{})
	for _, id := range request.HunkIDs {
		hunk, exists := hunksByID[id]
		if !exists {
			return Candidate{}, fmt.Errorf("%w: %s", ErrStaleHunk, id)
		}
		if _, overlap := fullPaths[hunk.Path]; overlap {
			return Candidate{}, fmt.Errorf("path %q is selected both wholly and by hunk", hunk.Path)
		}
		if request.RejectPath != nil && request.RejectPath(hunk.Path) {
			return Candidate{}, fmt.Errorf("selected path %q is generated or Workbench-owned", hunk.Path)
		}
		if _, duplicate := selectedIDs[id]; duplicate {
			return Candidate{}, fmt.Errorf("hunk %q appears more than once", id)
		}
		selectedIDs[id] = struct{}{}
		hunkPaths[hunk.Path] = struct{}{}
		selectedHunks = append(selectedHunks, hunk)
	}
	if len(fullPaths) == 0 && len(selectedHunks) == 0 {
		return Candidate{}, errors.New("repository selection is empty")
	}

	deletions, err := deletedPaths(ctx, root)
	if err != nil {
		return Candidate{}, err
	}
	acknowledged := make(map[string]struct{}, len(request.UnrelatedDeletedPaths))
	for _, path := range request.UnrelatedDeletedPaths {
		if err := validatePath(path); err != nil {
			return Candidate{}, err
		}
		if _, duplicate := acknowledged[path]; duplicate {
			return Candidate{}, fmt.Errorf("acknowledged deletion %q appears more than once", path)
		}
		acknowledged[path] = struct{}{}
	}
	for path := range acknowledged {
		if _, deleted := deletions[path]; !deleted {
			return Candidate{}, fmt.Errorf("acknowledged deletion %q is not currently deleted", path)
		}
		if _, selected := fullPaths[path]; selected {
			return Candidate{}, fmt.Errorf("selected deletion %q must not also be acknowledged as unrelated", path)
		}
	}
	for path := range deletions {
		if _, selected := fullPaths[path]; selected {
			continue
		}
		if _, partial := hunkPaths[path]; partial {
			return Candidate{}, fmt.Errorf("deleted path %q must be selected as a whole path", path)
		}
		if _, ok := acknowledged[path]; !ok {
			return Candidate{}, fmt.Errorf("%w: %s", ErrUnacknowledgedDeletion, path)
		}
	}

	indexPath, err := temporaryIndex()
	if err != nil {
		return Candidate{}, err
	}
	defer os.Remove(indexPath)
	shadow, err := shadowWorktree(root)
	if err != nil {
		return Candidate{}, fmt.Errorf("prepare isolated hook worktree: %w", err)
	}
	defer os.RemoveAll(shadow)
	shadowEnvironment, err := initializeShadowRepository(ctx, root, shadow, indexPath, request.Branch, startHEAD)
	if err != nil {
		return Candidate{}, fmt.Errorf("prepare isolated hook repository: %w", err)
	}
	if _, err := git(ctx, shadow, shadowEnvironment, nil, "read-tree", startHEAD); err != nil {
		return Candidate{}, err
	}
	if err := applySelectedHunks(ctx, shadow, shadowEnvironment, selectedHunks); err != nil {
		return Candidate{}, err
	}
	if len(request.Paths) != 0 {
		arguments := []string{"add", "-A", "--"}
		arguments = append(arguments, request.Paths...)
		if _, err := git(ctx, shadow, shadowEnvironment, nil, arguments...); err != nil {
			return Candidate{}, err
		}
	}
	allowed := make(map[string]struct{}, len(fullPaths)+len(hunkPaths))
	for path := range fullPaths {
		allowed[path] = struct{}{}
	}
	for path := range hunkPaths {
		allowed[path] = struct{}{}
	}
	if err := validateCandidatePaths(ctx, shadow, shadowEnvironment, allowed, request.RejectPath); err != nil {
		return Candidate{}, err
	}
	preparedPaths, err := changedPaths(ctx, shadow, shadowEnvironment, true)
	if err != nil {
		return Candidate{}, err
	}
	preparedSet := make(map[string]struct{}, len(preparedPaths))
	for _, path := range preparedPaths {
		preparedSet[path] = struct{}{}
	}
	for path := range fullPaths {
		if _, present := preparedSet[path]; !present {
			return Candidate{}, fmt.Errorf("selected path %q has no exact current change", path)
		}
	}
	hunkDiffs := make(map[string][]byte, len(hunkPaths))
	for path := range hunkPaths {
		diff, err := git(ctx, shadow, shadowEnvironment, nil, "diff", "--cached", "--binary", "HEAD", "--", path)
		if err != nil {
			return Candidate{}, err
		}
		hunkDiffs[path] = diff
	}
	statusBeforeHooks, err := git(ctx, root, nil, nil, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		return Candidate{}, err
	}
	message := renderMessage(request)
	messagePath, err := temporaryMessage(message)
	if err != nil {
		return Candidate{}, err
	}
	defer os.Remove(messagePath)
	if err := runCommitHooks(ctx, shadow, shadowEnvironment, messagePath); err != nil {
		return Candidate{}, err
	}
	statusAfterHooks, err := git(ctx, root, nil, nil, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil {
		return Candidate{}, err
	}
	if !bytes.Equal(statusBeforeHooks, statusAfterHooks) {
		return Candidate{}, errors.New("commit hook or concurrent writer changed the real worktree during isolated preflight")
	}
	afterHookHEAD, err := gitText(ctx, root, nil, nil, "rev-parse", "HEAD")
	if err != nil {
		return Candidate{}, err
	}
	if afterHookHEAD != startHEAD {
		return Candidate{}, errors.New("commit hook changed repository HEAD during preflight")
	}
	afterHookIndex, err := gitText(ctx, root, nil, nil, "write-tree")
	if err != nil {
		return Candidate{}, fmt.Errorf("observe real index after commit hooks: %w", err)
	}
	if afterHookIndex != startTree {
		return Candidate{}, errors.New("commit hook changed the real repository index during isolated preflight")
	}
	if err := validateCandidatePaths(ctx, shadow, shadowEnvironment, allowed, request.RejectPath); err != nil {
		return Candidate{}, fmt.Errorf("commit hook widened exact selection: %w", err)
	}
	for path, before := range hunkDiffs {
		after, err := git(ctx, shadow, shadowEnvironment, nil, "diff", "--cached", "--binary", "HEAD", "--", path)
		if err != nil {
			return Candidate{}, err
		}
		if !bytes.Equal(before, after) {
			return Candidate{}, fmt.Errorf("commit hook changed exact selected hunks in %q", path)
		}
	}
	messageBytes, err := os.ReadFile(messagePath)
	if err != nil {
		return Candidate{}, fmt.Errorf("read prepared commit message: %w", err)
	}
	trailer := "Workbench-Change-Id: " + request.ChangeID
	if !hasExactChangeTrailer(string(messageBytes), trailer) {
		return Candidate{}, fmt.Errorf("commit hooks must preserve exactly one %q trailer", trailer)
	}
	shadowTree, err := gitText(ctx, shadow, shadowEnvironment, nil, "write-tree")
	if err != nil {
		return Candidate{}, err
	}
	if shadowTree == startTree {
		return Candidate{}, errors.New("exact selection produces no commit change")
	}
	tree, err := promoteCandidateTree(ctx, root, shadow, shadowEnvironment, indexPath, startHEAD, shadowTree)
	if err != nil {
		return Candidate{}, err
	}
	remote, err := remoteHEAD(ctx, root, request.Remote, request.Branch)
	if err != nil {
		return Candidate{}, err
	}
	remoteURL, err := observeRemoteURL(ctx, root, request.Remote)
	if err != nil {
		return Candidate{}, err
	}
	digest, err := requestDigest(request, root)
	if err != nil {
		return Candidate{}, err
	}
	return Candidate{
		ResourceID: request.ResourceID, Repository: root, Branch: request.Branch,
		Remote: request.Remote, RemoteURL: remoteURL, ChangeID: request.ChangeID, RequestDigest: digest,
		StatusDigest: digestBytes(statusBeforeHooks), StartHEAD: startHEAD, StartTree: startTree,
		StartRemote: remote, Tree: tree, Message: string(messageBytes),
	}, nil
}

// shadowWorktree copies the caller-visible worktree without its Git control
// directory. Hooks receive the real repository configuration and candidate
// index through explicit Git variables, while every ordinary filesystem write
// is confined to this disposable shadow.
func shadowWorktree(root string) (string, error) {
	shadow, err := os.MkdirTemp("", "workbench-hook-worktree-*")
	if err != nil {
		return "", err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.RemoveAll(shadow)
		}
	}()
	if err := copyDirectory(root, shadow, true); err != nil {
		return "", err
	}
	succeeded = true
	return shadow, nil
}

func copyDirectory(root, destinationRoot string, skipRootGit bool) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if skipRootGit && relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if relative == "." {
			return nil
		}
		target := filepath.Join(destinationRoot, relative)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		}
		if entry.IsDir() {
			return os.Mkdir(target, info.Mode().Perm())
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("worktree path %q has unsupported mode %s", relative, info.Mode())
		}
		source, err := os.Open(path)
		if err != nil {
			return err
		}
		destination, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			source.Close()
			return err
		}
		_, copyErr := io.Copy(destination, source)
		sourceCloseErr := source.Close()
		closeErr := destination.Close()
		if copyErr != nil {
			return copyErr
		}
		if sourceCloseErr != nil {
			return sourceCloseErr
		}
		return closeErr
	})
}

func initializeShadowRepository(ctx context.Context, original, shadow, indexPath, branch, head string) ([]string, error) {
	gitDirectory := filepath.Join(shadow, ".git")
	for _, directory := range []string{
		filepath.Join(gitDirectory, "objects", "info"),
		filepath.Join(gitDirectory, "objects", "pack"),
		filepath.Join(gitDirectory, "refs", "heads"),
		filepath.Join(gitDirectory, "refs", "tags"),
		filepath.Join(gitDirectory, "hooks"),
	} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, err
		}
	}
	config := "[core]\n\trepositoryformatversion = 0\n\tfilemode = true\n\tbare = false\n\tlogallrefupdates = false\n"
	if err := os.WriteFile(filepath.Join(gitDirectory, "config"), []byte(config), 0o600); err != nil {
		return nil, err
	}
	originalGitDirectory, err := gitText(ctx, original, nil, nil, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return nil, err
	}
	if err := copyHookConfiguration(ctx, original, originalGitDirectory, filepath.Join(gitDirectory, "config")); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(gitDirectory, "HEAD"), []byte("ref: refs/heads/"+branch+"\n"), 0o600); err != nil {
		return nil, err
	}
	originalObjects, err := gitText(ctx, original, nil, nil, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(gitDirectory, "objects", "info", "alternates"), []byte(originalObjects+"\n"), 0o600); err != nil {
		return nil, err
	}
	originalHooks, err := gitText(ctx, original, nil, nil, "rev-parse", "--path-format=absolute", "--git-path", "hooks")
	if err != nil {
		return nil, err
	}
	if info, statErr := os.Stat(originalHooks); statErr == nil && info.IsDir() {
		if err := copyDirectory(originalHooks, filepath.Join(gitDirectory, "hooks"), false); err != nil {
			return nil, err
		}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return nil, statErr
	}

	environment := []string{
		"GIT_DIR=" + gitDirectory,
		"GIT_WORK_TREE=" + shadow,
		"GIT_INDEX_FILE=" + indexPath,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
	}
	if _, err := git(ctx, shadow, environment, nil, "update-ref", "refs/heads/"+branch, head); err != nil {
		return nil, err
	}
	return environment, nil
}

func copyHookConfiguration(ctx context.Context, original, originalGitDirectory, destination string) error {
	encoded, err := git(ctx, original, nil, nil, "config", "--local", "--null", "--list")
	if err != nil {
		return err
	}
	for _, entry := range bytes.Split(encoded, []byte{0}) {
		if len(entry) == 0 {
			continue
		}
		key, value, ok := strings.Cut(string(entry), "\n")
		if !ok {
			return fmt.Errorf("local Git configuration entry %q has no value", entry)
		}
		lower := strings.ToLower(key)
		if unsafeHookConfigKey(lower) || strings.Contains(value, originalGitDirectory) || strings.Contains(value, original) {
			continue
		}
		command := exec.CommandContext(ctx, "git", "config", "--file", destination, "--add", key, value)
		command.Env = environmentWithOverrides(os.Environ(), []string{"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + os.DevNull})
		if output, err := command.CombinedOutput(); err != nil {
			return fmt.Errorf("copy hook configuration %q: %w: %s", key, err, strings.TrimSpace(string(output)))
		}
	}
	return nil
}

func unsafeHookConfigKey(key string) bool {
	for _, prefix := range []string{"remote.", "branch.", "submodule.", "url.", "credential.", "http.", "include.", "includeif.", "safe."} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	switch key {
	case "core.repositoryformatversion", "core.filemode", "core.bare", "core.logallrefupdates", "core.worktree", "core.hookspath", "core.sshcommand", "core.gitproxy", "extensions.worktreeconfig":
		return true
	default:
		return false
	}
}

func promoteCandidateTree(ctx context.Context, original, shadow string, shadowEnvironment []string, indexPath, head, expectedTree string) (string, error) {
	patch, err := git(ctx, shadow, shadowEnvironment, nil, "diff", "--cached", "--binary", "HEAD", "--")
	if err != nil {
		return "", fmt.Errorf("export accepted isolated candidate: %w", err)
	}
	productionEnvironment := []string{"GIT_INDEX_FILE=" + indexPath}
	if _, err := git(ctx, original, productionEnvironment, nil, "read-tree", head); err != nil {
		return "", fmt.Errorf("initialize accepted production candidate: %w", err)
	}
	if _, err := git(ctx, original, productionEnvironment, patch, "apply", "--cached", "--binary", "--whitespace=nowarn", "-"); err != nil {
		return "", fmt.Errorf("materialize accepted production candidate: %w", err)
	}
	tree, err := gitText(ctx, original, productionEnvironment, nil, "write-tree")
	if err != nil {
		return "", err
	}
	if tree != expectedTree {
		return "", fmt.Errorf("accepted isolated tree %s became %s during production materialization", expectedTree, tree)
	}
	return tree, nil
}

func hasExactChangeTrailer(message, expected string) bool {
	count := 0
	for _, line := range strings.Split(strings.ReplaceAll(message, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "Workbench-Change-Id:") {
			if line != expected {
				return false
			}
			count++
		}
	}
	return count == 1
}

func validateRequest(request Request) error {
	for label, value := range map[string]string{
		"resource identity": request.ResourceID, "repository": request.Repository,
		"branch": request.Branch, "remote": request.Remote, "change identifier": request.ChangeID,
		"title": request.Title,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", label)
		}
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("%s contains a forbidden line break", label)
		}
	}
	if strings.HasPrefix(request.Remote, "-") {
		return errors.New("remote designation must not begin with an option prefix")
	}
	if (request.RejectPath == nil) != (request.GeneratedPathPolicyID == "") {
		return errors.New("generated-path rejection and its immutable policy identity must be supplied together")
	}
	return nil
}

func validatePath(path string) error {
	if path == "" || filepath.IsAbs(path) || filepath.ToSlash(filepath.Clean(path)) != path || path == ".git" || strings.HasPrefix(path, ".git/") || path == ".." || strings.HasPrefix(path, "../") {
		return fmt.Errorf("path %q is not a normalized repository-relative path", path)
	}
	return nil
}

func renderMessage(request Request) string {
	parts := []string{request.Title}
	if description := strings.TrimSpace(request.Description); description != "" {
		parts = append(parts, description)
	}
	parts = append(parts, "Workbench-Change-Id: "+request.ChangeID)
	return strings.Join(parts, "\n\n") + "\n"
}

func requestDigest(request Request, canonicalRepository string) (string, error) {
	paths := append([]string(nil), request.Paths...)
	hunks := append([]string(nil), request.HunkIDs...)
	deletions := append([]string(nil), request.UnrelatedDeletedPaths...)
	sort.Strings(paths)
	sort.Strings(hunks)
	sort.Strings(deletions)
	value := struct {
		ResourceID, Repository, Branch, Remote, ChangeID, Title, Description, GeneratedPathPolicyID string
		Paths, HunkIDs, UnrelatedDeletedPaths                                                       []string
	}{request.ResourceID, canonicalRepository, request.Branch, request.Remote, request.ChangeID, request.Title, request.Description, request.GeneratedPathPolicyID, paths, hunks, deletions}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encode request digest: %w", err)
	}
	return digestBytes(encoded), nil
}

func repositoryRoot(ctx context.Context, designation string) (string, error) {
	abs, err := filepath.Abs(designation)
	if err != nil {
		return "", fmt.Errorf("resolve repository path: %w", err)
	}
	root, err := gitText(ctx, abs, nil, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", err
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve repository root: %w", err)
	}
	resolvedInput, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", fmt.Errorf("resolve repository designation: %w", err)
	}
	if resolvedRoot != resolvedInput {
		return "", fmt.Errorf("repository designation %q is not its canonical root %q", designation, resolvedRoot)
	}
	return resolvedRoot, nil
}

func temporaryIndex() (string, error) {
	file, err := os.CreateTemp("", "workbench-index-*")
	if err != nil {
		return "", fmt.Errorf("reserve alternate index: %w", err)
	}
	path := file.Name()
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close alternate index reservation: %w", err)
	}
	if err := os.Remove(path); err != nil {
		return "", fmt.Errorf("initialize alternate index: %w", err)
	}
	return path, nil
}

func temporaryMessage(message string) (string, error) {
	file, err := os.CreateTemp("", "workbench-message-*")
	if err != nil {
		return "", fmt.Errorf("create commit message: %w", err)
	}
	path := file.Name()
	if _, err := file.WriteString(message); err != nil {
		file.Close()
		return "", fmt.Errorf("write commit message: %w", err)
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return "", fmt.Errorf("sync commit message: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close commit message: %w", err)
	}
	return path, nil
}

func runCommitHooks(ctx context.Context, root string, environment []string, messagePath string) error {
	hooks := []struct {
		name      string
		arguments []string
	}{
		{"pre-commit", nil},
		{"prepare-commit-msg", []string{messagePath, "message"}},
		{"commit-msg", []string{messagePath}},
	}
	for _, hook := range hooks {
		path, err := gitText(ctx, root, environment, nil, "rev-parse", "--path-format=absolute", "--git-path", "hooks/"+hook.name)
		if err != nil {
			return err
		}
		info, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("observe %s hook: %w", hook.name, err)
		}
		if info.Mode()&0o111 == 0 {
			continue
		}
		arguments := []string{"hook", "run", hook.name}
		if len(hook.arguments) != 0 {
			arguments = append(arguments, "--")
			arguments = append(arguments, hook.arguments...)
		}
		if _, err := git(ctx, root, environment, nil, arguments...); err != nil {
			return fmt.Errorf("%s hook failed: %w", hook.name, err)
		}
	}
	return nil
}

func validateCandidatePaths(ctx context.Context, root string, environment []string, allowed map[string]struct{}, reject func(string) bool) error {
	paths, err := changedPaths(ctx, root, environment, true)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return errors.New("exact selection is empty")
	}
	for _, path := range paths {
		if _, ok := allowed[path]; !ok {
			return fmt.Errorf("unselected path %q entered the candidate", path)
		}
		if reject != nil && reject(path) {
			return fmt.Errorf("generated or Workbench-owned path %q entered the candidate", path)
		}
	}
	return nil
}

func deletedPaths(ctx context.Context, root string) (map[string]struct{}, error) {
	output, err := git(ctx, root, nil, nil, "diff", "--name-only", "-z", "--diff-filter=D", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	result := make(map[string]struct{})
	for _, path := range splitNUL(output) {
		result[path] = struct{}{}
	}
	return result, nil
}

func changedPaths(ctx context.Context, root string, environment []string, cached bool) ([]string, error) {
	arguments := []string{"diff"}
	if cached {
		arguments = append(arguments, "--cached")
	}
	arguments = append(arguments, "--name-only", "-z", "HEAD", "--")
	output, err := git(ctx, root, environment, nil, arguments...)
	if err != nil {
		return nil, err
	}
	return splitNUL(output), nil
}

func splitNUL(output []byte) []string {
	parts := bytes.Split(output, []byte{0})
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) != 0 {
			result = append(result, string(part))
		}
	}
	return result
}

func parseAllHunks(ctx context.Context, root string) ([]parsedHunk, error) {
	paths, err := changedPaths(ctx, root, nil, false)
	if err != nil {
		return nil, err
	}
	var result []parsedHunk
	for _, path := range paths {
		hunks, err := hunksForPath(ctx, root, path)
		if err != nil {
			return nil, err
		}
		result = append(result, hunks...)
	}
	return result, nil
}

func hunksForPath(ctx context.Context, root, path string) ([]parsedHunk, error) {
	diff, err := git(ctx, root, nil, nil, "diff", "--binary", "--no-ext-diff", "HEAD", "--", path)
	if err != nil {
		return nil, err
	}
	if len(diff) == 0 {
		return nil, nil
	}
	text := string(diff)
	if strings.Contains(text, "GIT binary patch") || strings.Contains(text, "Binary files ") {
		return nil, nil
	}
	lines := strings.SplitAfter(text, "\n")
	firstHunk := -1
	for index, line := range lines {
		if strings.HasPrefix(line, "@@ ") {
			firstHunk = index
			break
		}
	}
	if firstHunk < 0 {
		return nil, nil
	}
	header := strings.Join(lines[:firstHunk], "")
	var result []parsedHunk
	for start := firstHunk; start < len(lines); {
		end := start + 1
		for end < len(lines) && !strings.HasPrefix(lines[end], "@@ ") {
			end++
		}
		body := strings.Join(lines[start:end], "")
		hash := sha256.Sum256([]byte(path + "\x00" + body))
		id := hex.EncodeToString(hash[:])[:12]
		result = append(result, parsedHunk{Hunk: Hunk{ID: id, Path: path, Diff: header + body}, patch: header + body})
		start = end
	}
	return result, nil
}

func applySelectedHunks(ctx context.Context, root string, environment []string, hunks []parsedHunk) error {
	patches := make(map[string]string)
	order := make([]string, 0, len(hunks))
	for _, hunk := range hunks {
		marker := strings.Index(hunk.patch, "@@ ")
		if marker < 0 {
			return fmt.Errorf("selected hunk %s has no unified-diff header", hunk.ID)
		}
		if _, exists := patches[hunk.Path]; !exists {
			patches[hunk.Path] = hunk.patch[:marker]
			order = append(order, hunk.Path)
		}
		patches[hunk.Path] += hunk.patch[marker:]
	}
	for _, path := range order {
		if _, err := git(ctx, root, environment, []byte(patches[path]), "apply", "--cached", "--whitespace=nowarn", "-"); err != nil {
			return fmt.Errorf("apply selected hunks for %s: %w", path, err)
		}
	}
	return nil
}

func remoteHEAD(ctx context.Context, root, remote, branch string) (string, error) {
	output, err := git(ctx, root, nil, nil, "ls-remote", "--heads", remote, "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(output))
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 {
		return "", fmt.Errorf("unexpected ls-remote response for %s", branch)
	}
	return fields[0], nil
}

func observeRemoteURL(ctx context.Context, root, remote string) (string, error) {
	output, err := git(ctx, root, nil, nil, "remote", "get-url", "--push", "--all", remote)
	if err != nil {
		return "", err
	}
	urls := strings.Fields(string(output))
	if len(urls) != 1 {
		return "", fmt.Errorf("remote %q must have exactly one push URL", remote)
	}
	parsed, err := url.Parse(urls[0])
	if err != nil {
		return "", fmt.Errorf("parse push URL for remote %q: %w", remote, err)
	}
	if parsed.User != nil {
		return "", fmt.Errorf("remote %q push URL must not contain embedded credentials", remote)
	}
	return urls[0], nil
}

func digestBytes(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

type gitError struct {
	arguments []string
	output    string
	cause     error
}

func (failure *gitError) Error() string {
	return fmt.Sprintf("git %s failed: %v: %s", strings.Join(failure.arguments, " "), failure.cause, strings.TrimSpace(failure.output))
}
func (failure *gitError) Unwrap() error { return failure.cause }

func gitText(ctx context.Context, root string, environment []string, input []byte, arguments ...string) (string, error) {
	output, err := git(ctx, root, environment, input, arguments...)
	return strings.TrimSpace(string(output)), err
}

func git(ctx context.Context, root string, environment []string, input []byte, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", arguments...)
	command.Dir = root
	command.Env = environmentWithOverrides(os.Environ(), environment)
	command.Stdin = bytes.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, &gitError{arguments: arguments, output: string(output), cause: err}
	}
	return output, nil
}

func environmentWithOverrides(base, overrides []string) []string {
	names := make(map[string]struct{}, len(overrides))
	for _, value := range overrides {
		name, _, _ := strings.Cut(value, "=")
		names[name] = struct{}{}
	}
	result := make([]string, 0, len(base)+len(overrides))
	for _, value := range base {
		name, _, _ := strings.Cut(value, "=")
		if _, overridden := names[name]; !overridden {
			result = append(result, value)
		}
	}
	return append(result, overrides...)
}
