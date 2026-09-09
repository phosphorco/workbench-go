// Package contextconfig owns Workbench declaration discovery and activation
// meaning. It has no evaluator, provider, cache, or runtime side effects.
package contextconfig

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"github.com/phosphorco/workbench-go/internal/contextcache"
)

const (
	projectDeclarationRelativePath = "workbench-context.pkl"
	sha256Prefix                   = "sha256:"
	invalidDigestPrefix            = "invalid:"
	maxHookDeadlineMs              = uint64(60 * 1000)
	maxWholeHookDeadlineMs         = uint64(5 * 60 * 1000)
	maxActivationAncestors         = uint32(4096)
	maxActivationConfigBytes       = uint64(64 * 1024 * 1024)
	maxActivationConfigFiles       = uint32(4096)
	maxConfiguredProviders         = uint32(4096)
	maxConfiguredCount             = uint32(1_000_000)
	maxContributionBodyBytes       = uint64(64 * 1024 * 1024)
	maxPendingMemoryBytes          = uint64(1024 * 1024 * 1024)
	maxQueueItemBytes              = uint64(64 * 1024 * 1024)
	maxQueuePendingBytes           = uint64(1024 * 1024 * 1024)
	maxQueueAgeMs                  = uint64(30 * 24 * 60 * 60 * 1000)
	maxProviderDeadlineMs          = uint64(60 * 1000)
	maxProviderBytes               = uint64(64 * 1024 * 1024)
	maxCacheDiskCapBytes           = uint64(64 * 1024 * 1024 * 1024)
	maxCacheIdleTTLMs              = uint64(365 * 24 * 60 * 60 * 1000)
	maxCacheBytes                  = uint64(256 * 1024 * 1024)
	maxCacheStringBytes            = uint64(1024 * 1024)
	defaultMaxRetainedBytes        = uint64(4 * 1024 * 1024)
)

// LoadLimits bounds discovery and bootstrap evaluation. A zero field is
// filled from DefaultLoadLimits; a valid home declaration may reduce these
// bounds after its own evaluation.
type LoadLimits struct {
	MaxAncestorDepth  int
	MaxProjectConfigs int
	MaxConfigBytes    int64
	MaxHomeScopes     int
	MaxContributors   int
	EvaluationBounds  contextapi.EvaluationBounds
}

func DefaultLoadLimits() LoadLimits {
	return LoadLimits{
		MaxAncestorDepth:  64,
		MaxProjectConfigs: 32,
		MaxConfigBytes:    256 * 1024,
		MaxHomeScopes:     64,
		MaxContributors:   64,
		EvaluationBounds: contextapi.EvaluationBounds{
			MaxInputBytes:       256 * 1024,
			MaxOutputBytes:      8 * 1024 * 1024,
			MaxDiagnosticsBytes: 1 * 1024 * 1024,
			MaxImports:          128,
			DeadlineMs:          5 * 1000,
		},
	}
}

// LoadOptions makes all discovery inputs explicit. Empty HomeConfigPath means
// no home declaration; the loader does not consult environment paths.
type LoadOptions struct {
	WorkingDirectory string
	HomeConfigPath   string
	Now              time.Time
	Limits           LoadLimits
}

// LoadDependencies is the only evaluator/cache seam. Freshness is the one
// composition-owned callback over the exact entry input and captured import
// closure. It is reused by snapshot reuse and the cache policy validator.
type LoadDependencies struct {
	Evaluate  func(context.Context, contextapi.EvaluationInput) (contextapi.EvaluatedDeclaration, error)
	Freshness func(context.Context, contextapi.EvaluationInput, contextapi.EvaluatedDeclaration) error
	Identity  func(context.Context) (contextapi.EvaluatorIdentity, error)
	Cache     *contextcache.Pool
}

// EvaluatedDeclarationCapture retains the exact entry SourceBytes alongside
// the evaluated value and dependency manifest. This prevents a snapshot from
// being freshened using imports alone while the entry module changed.
type EvaluatedDeclarationCapture struct {
	Input       contextapi.EvaluationInput
	Declaration contextapi.EvaluatedDeclaration
}

type LoadResult struct {
	Input       contextapi.ActivationInput
	Result      contextapi.ActivationResult
	Captures    []EvaluatedDeclarationCapture
	CachePolicy *contextcache.Policy `json:"-"`
}

var errInvalidLoadOptions = errors.New("invalid context configuration load options")

type loadIssue struct {
	reason contextapi.Reason
	err    error
}

type projectCandidate struct {
	snapshot contextapi.ProjectSnapshot
	depth    int
}

type homeCandidate struct {
	snapshot contextapi.HomeDirectorySnapshot
	depth    int
}

type snapshotRecord struct {
	Input       contextapi.EvaluationInput
	Declaration contextapi.EvaluatedDeclaration
}

// Load performs bounded Pkl source discovery and delegates evaluation to the
// caller. Legacy JSON files are never inspected. A present source without an
// evaluator fails closed as ActivationInvalid; absent sources take the zero
// dependency path and return inactive without calling any dependency.
func Load(ctx context.Context, options LoadOptions, deps LoadDependencies) (LoadResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return LoadResult{}, err
	}
	limits, err := normalizeLimits(options.Limits)
	if err != nil {
		return LoadResult{}, err
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	input := contextapi.ActivationInput{WorkingDirectory: options.WorkingDirectory, Now: now}
	issues := make([]loadIssue, 0, 2)

	workingDirectory, issue := canonicalDirectory(options.WorkingDirectory, now)
	if issue != nil {
		input.Config.ConfigDigest = invalidDigestPrefix + "working-directory"
		return invalidLoadResult(input, []loadIssue{{reason: *issue}}), nil
	}
	if err := ctx.Err(); err != nil {
		return LoadResult{}, err
	}
	input.WorkingDirectory = workingDirectory

	home, homeCapture, homeIssues, policy := loadHome(ctx, options.HomeConfigPath, limits, deps, now)
	if err := cancellationFromIssues(homeIssues); err != nil {
		return LoadResult{}, err
	}
	input.Config.Home = home
	issues = append(issues, homeIssues...)
	if len(homeIssues) == 0 && homeCapture != nil {
		limits = applyRuntimeLoadBounds(limits, home.Runtime)
	}
	var projects []contextapi.ProjectSnapshot
	var projectCaptures []EvaluatedDeclarationCapture
	var projectIssues []loadIssue
	if len(homeIssues) == 0 {
		projects, projectCaptures, projectIssues = loadProjects(ctx, workingDirectory, limits, deps, policy, now)
	}
	if err := cancellationFromIssues(projectIssues); err != nil {
		return LoadResult{}, err
	}
	input.Config.Projects = projects
	issues = append(issues, projectIssues...)
	captures := make([]EvaluatedDeclarationCapture, 0, len(projectCaptures)+1)
	if homeCapture != nil {
		captures = append(captures, *homeCapture)
	}
	captures = append(captures, projectCaptures...)
	input.Config.ConfigDigest = activationDigest(home, projects, issues)
	if err := ctx.Err(); err != nil {
		return LoadResult{}, err
	}

	result := Resolve(input)
	if len(issues) > 0 {
		result = invalidResult(reasonsFromIssues(issues))
	}
	return LoadResult{Input: input, Result: result, Captures: captures, CachePolicy: policy}, nil
}

func normalizeLimits(input LoadLimits) (LoadLimits, error) {
	defaults := DefaultLoadLimits()
	if input.MaxAncestorDepth == 0 {
		input.MaxAncestorDepth = defaults.MaxAncestorDepth
	}
	if input.MaxProjectConfigs == 0 {
		input.MaxProjectConfigs = defaults.MaxProjectConfigs
	}
	if input.MaxConfigBytes == 0 {
		input.MaxConfigBytes = defaults.MaxConfigBytes
	}
	if input.MaxHomeScopes == 0 {
		input.MaxHomeScopes = defaults.MaxHomeScopes
	}
	if input.MaxContributors == 0 {
		input.MaxContributors = defaults.MaxContributors
	}
	bounds := input.EvaluationBounds
	if bounds.MaxInputBytes == 0 {
		bounds.MaxInputBytes = defaults.EvaluationBounds.MaxInputBytes
	}
	if bounds.MaxOutputBytes == 0 {
		bounds.MaxOutputBytes = defaults.EvaluationBounds.MaxOutputBytes
	}
	if bounds.MaxDiagnosticsBytes == 0 {
		bounds.MaxDiagnosticsBytes = defaults.EvaluationBounds.MaxDiagnosticsBytes
	}
	if bounds.MaxImports == 0 {
		bounds.MaxImports = defaults.EvaluationBounds.MaxImports
	}
	if bounds.DeadlineMs == 0 {
		bounds.DeadlineMs = defaults.EvaluationBounds.DeadlineMs
	}
	input.EvaluationBounds = bounds
	if input.MaxAncestorDepth < 1 || input.MaxProjectConfigs < 1 || input.MaxConfigBytes < 1 || input.MaxHomeScopes < 1 || input.MaxContributors < 1 || bounds.MaxInputBytes < 1 || bounds.MaxOutputBytes < 1 || bounds.MaxDiagnosticsBytes < 1 || bounds.MaxImports < 1 || bounds.DeadlineMs < 1 {
		return LoadLimits{}, errInvalidLoadOptions
	}
	return input, nil
}

func applyRuntimeLoadBounds(limits LoadLimits, runtime contextapi.RuntimeLimits) LoadLimits {
	if runtime.MaxActivationAncestors > 0 && uint64(limits.MaxAncestorDepth) > uint64(runtime.MaxActivationAncestors) {
		limits.MaxAncestorDepth = int(runtime.MaxActivationAncestors)
	}
	if runtime.MaxActivationConfigFiles > 0 && uint64(limits.MaxProjectConfigs) > uint64(runtime.MaxActivationConfigFiles) {
		limits.MaxProjectConfigs = int(runtime.MaxActivationConfigFiles)
	}
	if runtime.MaxActivationConfigBytes > 0 && limits.MaxConfigBytes > int64(runtime.MaxActivationConfigBytes) {
		limits.MaxConfigBytes = int64(runtime.MaxActivationConfigBytes)
	}
	if runtime.MaxProviders > 0 && uint64(limits.MaxContributors) > uint64(runtime.MaxProviders) {
		limits.MaxContributors = int(runtime.MaxProviders)
	}
	if runtime.MaxActivationConfigBytes > 0 && limits.EvaluationBounds.MaxInputBytes > runtime.MaxActivationConfigBytes {
		limits.EvaluationBounds.MaxInputBytes = runtime.MaxActivationConfigBytes
	}
	return limits
}

func canonicalDirectory(input string, now time.Time) (string, *contextapi.Reason) {
	if input == "" || !filepath.IsAbs(input) {
		reason := makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonAdapter, "working directory must be an absolute path", now)
		return "", &reason
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(input))
	if err != nil {
		reason := makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonAdapter, fmt.Sprintf("working directory cannot be canonicalized: %v", err), now)
		return "", &reason
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		message := "working directory is not a directory"
		if err != nil {
			message = fmt.Sprintf("working directory cannot be read: %v", err)
		}
		reason := makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonAdapter, message, now)
		return "", &reason
	}
	return filepath.Clean(canonical), nil
}

func loadHome(ctx context.Context, path string, limits LoadLimits, deps LoadDependencies, now time.Time) (contextapi.HomeSnapshot, *EvaluatedDeclarationCapture, []loadIssue, *contextcache.Policy) {
	if err := ctx.Err(); err != nil {
		return contextapi.HomeSnapshot{}, nil, []loadIssue{{err: err, reason: makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonAdapter, err.Error(), now)}}, nil
	}
	if path == "" {
		return contextapi.HomeSnapshot{}, nil, nil, defaultHomePolicy("", deps)
	}
	raw, canonicalPath, exists, err := readOptionalSource(ctx, path, limits.MaxConfigBytes)
	if !exists && err == nil {
		return contextapi.HomeSnapshot{}, nil, nil, defaultHomePolicy(path, deps)
	}
	if err != nil {
		return contextapi.HomeSnapshot{}, nil, []loadIssue{{err: err, reason: makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("home declaration %q cannot be loaded: %v", path, err), now)}}, nil
	}
	root := filepath.Dir(canonicalPath)
	value, capture, cacheHit, err := evaluateOrReadSnapshot(ctx, raw, canonicalPath, root, contextapi.ScopeAuthorityHome, "workbench:context-home", deps, limits.EvaluationBounds, true, nil)
	if err != nil {
		return contextapi.HomeSnapshot{}, nil, []loadIssue{{err: err, reason: makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("home declaration %q is invalid: %v", canonicalPath, err), now)}}, nil
	}
	if value.Kind != contextapi.DeclarationKindHome {
		return contextapi.HomeSnapshot{}, nil, []loadIssue{{reason: makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, "home declaration evaluated as project", now)}}, nil
	}
	if err := validateHomeDeclaration(value.Home, limits); err != nil {
		return contextapi.HomeSnapshot{}, nil, []loadIssue{{reason: makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("home declaration %q is invalid: %v", canonicalPath, err), now)}}, nil
	}
	home, err := homeSnapshot(value)
	if err != nil {
		return contextapi.HomeSnapshot{}, nil, []loadIssue{{reason: makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("home declaration %q has invalid selection: %v", canonicalPath, err), now)}}, nil
	}
	policy := presentHomePolicy(path, deps, *capture, home)
	if !cacheHit {
		publishSnapshot(ctx, deps, *capture, policy, true)
	}
	return home, capture, nil, policy
}

func loadProjects(ctx context.Context, workingDirectory string, limits LoadLimits, deps LoadDependencies, policy *contextcache.Policy, now time.Time) ([]contextapi.ProjectSnapshot, []EvaluatedDeclarationCapture, []loadIssue) {
	projects := make([]contextapi.ProjectSnapshot, 0)
	captures := make([]EvaluatedDeclarationCapture, 0)
	issues := make([]loadIssue, 0)
	current := workingDirectory
	for depth := 0; ; depth++ {
		if err := ctx.Err(); err != nil {
			issues = append(issues, loadIssue{err: err, reason: makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonAdapter, err.Error(), now)})
			break
		}
		if depth >= limits.MaxAncestorDepth {
			issues = append(issues, loadIssue{reason: makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonAdapter, fmt.Sprintf("ancestor lookup exceeded bound %d", limits.MaxAncestorDepth), now)})
			break
		}
		candidatePath := filepath.Join(current, projectDeclarationRelativePath)
		raw, canonicalPath, exists, err := readOptionalSource(ctx, candidatePath, limits.MaxConfigBytes)
		if err != nil {
			if exists || !os.IsNotExist(err) {
				issues = append(issues, loadIssue{err: err, reason: makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("project declaration %q cannot be loaded: %v", candidatePath, err), now)})
				break
			}
		} else if exists {
			if len(projects) >= limits.MaxProjectConfigs {
				issues = append(issues, loadIssue{reason: makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonAdapter, fmt.Sprintf("project declaration count exceeded bound %d", limits.MaxProjectConfigs), now)})
				break
			}
			value, capture, cacheHit, evalErr := evaluateOrReadSnapshot(ctx, raw, canonicalPath, current, contextapi.ScopeAuthorityProject, "workbench:context", deps, limits.EvaluationBounds, true, policy)
			if evalErr != nil {
				issues = append(issues, loadIssue{err: evalErr, reason: makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("project declaration %q is invalid: %v", canonicalPath, evalErr), now)})
				break
			}
			if value.Kind != contextapi.DeclarationKindProject {
				issues = append(issues, loadIssue{reason: makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("project declaration %q evaluated as home", canonicalPath), now)})
				break
			}
			if err := validateProjectDeclaration(value.Project, limits); err != nil {
				issues = append(issues, loadIssue{reason: makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("project declaration %q is invalid: %v", canonicalPath, err), now)})
				break
			}
			projects = append(projects, contextapi.ProjectSnapshot{SourcePath: canonicalPath, Scope: makeScopeIdentity(contextapi.ScopeAuthorityProject, current, value.Revision), Config: cloneProjectDeclaration(value.Project)})
			captures = append(captures, *capture)
			if !cacheHit {
				publishSnapshot(ctx, deps, *capture, policy, false)
			}
			break
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return projects, captures, issues
}

func readOptionalSource(ctx context.Context, path string, maxBytes int64) ([]byte, string, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", false, err
	}
	if path == "" || !filepath.IsAbs(path) {
		return nil, "", false, fmt.Errorf("source path must be absolute")
	}
	declaredPath := filepath.Clean(path)
	_, err := os.Lstat(declaredPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, "", false, nil
		}
		return nil, declaredPath, true, err
	}
	root, err := os.OpenRoot(filepath.Dir(declaredPath))
	if err != nil {
		return nil, declaredPath, true, err
	}
	defer root.Close()
	file, err := root.OpenFile(filepath.Base(declaredPath), os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, declaredPath, true, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, declaredPath, true, err
	}
	if !info.Mode().IsRegular() {
		return nil, declaredPath, true, fmt.Errorf("not a regular file")
	}
	contents, err := readBoundedSource(ctx, file, maxBytes)
	if err != nil {
		return nil, declaredPath, true, err
	}
	return contents, declaredPath, true, nil
}

func readBoundedSource(ctx context.Context, file *os.File, maxBytes int64) ([]byte, error) {
	capacity := 32 * 1024
	if maxBytes < int64(capacity) {
		capacity = int(maxBytes)
	}
	contents := make([]byte, 0, capacity)
	buffer := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		read, err := file.Read(buffer)
		if read > 0 {
			contents = append(contents, buffer[:read]...)
			if int64(len(contents)) > maxBytes {
				return nil, fmt.Errorf("file exceeds %d-byte bound", maxBytes)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return contents, nil
			}
			return nil, err
		}
	}
}

func defaultHomePolicy(path string, _ LoadDependencies) *contextcache.Policy {
	capBytes := int64(resolveCacheLimits(contextapi.CacheLimits{}).DiskCapBytes)
	return &contextcache.Policy{CapBytes: capBytes, Validate: func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == "" {
			return nil
		}
		_, err := os.Lstat(filepath.Clean(path))
		exists := err == nil
		if err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("home declaration presence cannot be checked: %w", err)
		}
		if exists {
			return errors.New("home declaration appeared after absence was captured")
		}
		return nil
	}}
}

func presentHomePolicy(_ string, deps LoadDependencies, capture EvaluatedDeclarationCapture, snapshot contextapi.HomeSnapshot) *contextcache.Policy {
	capBytes := snapshot.Cache.DiskCapBytes
	if capBytes == 0 {
		capBytes = resolveCacheLimits(snapshot.Cache).DiskCapBytes
	}
	ownedCapture := cloneEvaluatedCapture(capture)
	return &contextcache.Policy{CapBytes: int64(capBytes), Validate: func(ctx context.Context) error {
		if deps.Freshness == nil {
			return errors.New("declaration freshness dependency is unavailable")
		}
		return deps.Freshness(ctx, ownedCapture.Input, ownedCapture.Declaration)
	}}
}

func evaluateOrReadSnapshot(ctx context.Context, raw []byte, path, root string, authority contextapi.ScopeAuthority, schema string, deps LoadDependencies, bounds contextapi.EvaluationBounds, cacheAllowed bool, policy *contextcache.Policy) (contextapi.EvaluatedDeclaration, *EvaluatedDeclarationCapture, bool, error) {
	input := contextapi.EvaluationInput{Origin: contextapi.DeclarationOrigin{Path: filepath.Clean(path), Root: filepath.Clean(root), Authority: authority}, SchemaURI: schema, SourceBytes: append([]byte(nil), raw...), Bounds: bounds}
	if err := input.Validate(); err != nil {
		return contextapi.EvaluatedDeclaration{}, nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return contextapi.EvaluatedDeclaration{}, nil, false, err
	}
	var identity contextapi.EvaluatorIdentity
	if deps.Identity != nil {
		var err error
		identity, err = deps.Identity(ctx)
		if err != nil {
			return contextapi.EvaluatedDeclaration{}, nil, false, err
		}
		if err := ctx.Err(); err != nil {
			return contextapi.EvaluatedDeclaration{}, nil, false, err
		}
	}
	key := snapshotKey(input, identity)
	if cacheAllowed && deps.Cache != nil && deps.Freshness != nil && key != "" {
		if encoded, found, err := deps.Cache.SnapshotRead(ctx, key); err == nil && found {
			var record snapshotRecord
			if json.Unmarshal(encoded, &record) == nil && bytesEqual(input.SourceBytes, record.Input.SourceBytes) && record.Input.Origin == input.Origin && record.Input.SchemaURI == input.SchemaURI && record.Declaration.Validate() == nil {
				fresh := error(nil)
				if policy != nil && policy.Validate != nil {
					fresh = policy.Validate(ctx)
				}
				if fresh == nil {
					fresh = deps.Freshness(ctx, input, record.Declaration)
				}
				if isCancellation(fresh) {
					return contextapi.EvaluatedDeclaration{}, nil, false, fresh
				}
				if fresh == nil {
					return record.Declaration, &EvaluatedDeclarationCapture{Input: cloneEvaluationInput(input), Declaration: record.Declaration}, true, nil
				}
			}
		}
	}
	if deps.Evaluate == nil {
		return contextapi.EvaluatedDeclaration{}, nil, false, errors.New("declaration evaluator dependency is unavailable")
	}
	value, err := deps.Evaluate(ctx, input)
	if err != nil {
		return contextapi.EvaluatedDeclaration{}, nil, false, err
	}
	if err := ctx.Err(); err != nil {
		return contextapi.EvaluatedDeclaration{}, nil, false, err
	}
	if err := value.Validate(); err != nil {
		return contextapi.EvaluatedDeclaration{}, nil, false, err
	}
	if value.Origin != input.Origin {
		return contextapi.EvaluatedDeclaration{}, nil, false, errors.New("evaluator returned a different declaration origin")
	}
	if identity != (contextapi.EvaluatorIdentity{}) && value.Evaluator != identity {
		return contextapi.EvaluatedDeclaration{}, nil, false, errors.New("evaluator identity changed during load")
	}
	wantKind := contextapi.DeclarationKindProject
	if authority == contextapi.ScopeAuthorityHome {
		wantKind = contextapi.DeclarationKindHome
	}
	if value.Kind != wantKind {
		return contextapi.EvaluatedDeclaration{}, nil, false, fmt.Errorf("evaluator returned %s for %s authority", value.Kind, authority)
	}
	value = cloneEvaluatedDeclaration(value)
	if policy != nil && policy.Validate != nil {
		if err := policy.Validate(ctx); err != nil {
			return contextapi.EvaluatedDeclaration{}, nil, false, err
		}
	}
	if deps.Freshness == nil {
		return contextapi.EvaluatedDeclaration{}, nil, false, errors.New("declaration freshness dependency is unavailable")
	}
	if err := deps.Freshness(ctx, input, value); err != nil {
		return contextapi.EvaluatedDeclaration{}, nil, false, err
	}
	return value, &EvaluatedDeclarationCapture{Input: cloneEvaluationInput(input), Declaration: value}, false, nil
}

func publishSnapshot(ctx context.Context, deps LoadDependencies, capture EvaluatedDeclarationCapture, policy *contextcache.Policy, policyOwnsCapture bool) {
	if deps.Cache == nil || deps.Freshness == nil || policy == nil || policy.Validate == nil || capture.Declaration.Evaluator.Digest == "" {
		return
	}
	key := snapshotKey(capture.Input, capture.Declaration.Evaluator)
	if key == "" {
		return
	}
	encoded, err := json.Marshal(snapshotRecord{Input: capture.Input, Declaration: capture.Declaration})
	if err != nil {
		return
	}
	if policy.CapBytes <= 0 {
		return
	}
	cachePolicy := contextcache.Policy{CapBytes: policy.CapBytes, Validate: func(policyCtx context.Context) error {
		if err := policy.Validate(policyCtx); err != nil {
			return err
		}
		if policyOwnsCapture {
			return nil
		}
		return deps.Freshness(policyCtx, capture.Input, capture.Declaration)
	}}
	_ = deps.Cache.SnapshotPublish(ctx, cachePolicy, key, encoded)
}

func snapshotKey(input contextapi.EvaluationInput, identity contextapi.EvaluatorIdentity) contextcache.SnapshotKey {
	if identity.Name == "" || identity.Version == "" || identity.Digest == "" {
		return ""
	}
	digest := digestString(string(input.Origin.Authority) + "\x00" + input.Origin.Path + "\x00" + input.Origin.Root + "\x00" + input.SchemaURI + "\x00" + identity.Name + "\x00" + identity.Version + "\x00" + string(identity.Digest))
	key, err := contextcache.NewSnapshotKey(digest)
	if err != nil {
		return ""
	}
	return key
}

func cloneEvaluationInput(input contextapi.EvaluationInput) contextapi.EvaluationInput {
	input.Origin.Path = strings.Clone(input.Origin.Path)
	input.Origin.Root = strings.Clone(input.Origin.Root)
	input.SchemaURI = strings.Clone(input.SchemaURI)
	input.SourceBytes = append([]byte(nil), input.SourceBytes...)
	return input
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// Resolve is pure: it reads only the supplied canonical snapshot and never
// evaluates Pkl, accesses the filesystem, starts providers, or writes cache.
func Resolve(input contextapi.ActivationInput) contextapi.ActivationResult {
	if reasons := validateSnapshot(input.Config); len(reasons) > 0 {
		return invalidResult(reasons)
	}
	workingDirectory := filepath.Clean(input.WorkingDirectory)
	homeMatches := matchingHomeScopes(input.Config.Home.Scopes, workingDirectory)
	for _, exclusion := range input.Config.Home.Exclusions {
		scope := contextapi.DeclarationScopeDirectory
		if exclusion.IncludeChildren {
			scope = contextapi.DeclarationScopeSubtree
		}
		if pathMatches(exclusion.Root, scope, workingDirectory) {
			reason := makeReason(contextapi.ReasonExcluded, contextapi.ReasonConfigured, fmt.Sprintf("home exclusion %q dominates project declarations", exclusion.Root), input.Now)
			return inactiveResult(homeScopeIdentity(homeMatches), []contextapi.Reason{reason})
		}
	}
	if conflict := duplicateHomeConflict(homeMatches); conflict != nil {
		return conflictResult(*conflict)
	}
	for _, candidate := range homeMatches {
		if !candidate.snapshot.Config.Enabled {
			reason := makeReason(contextapi.ReasonInactive, contextapi.ReasonConfigured, fmt.Sprintf("nearest home scope %q is disabled", candidate.snapshot.Scope.CanonicalRoot), input.Now)
			return inactiveResult(candidate.snapshot.Scope, []contextapi.Reason{reason})
		}
	}

	projects := matchingProjectBoundaries(input.Config.Projects, workingDirectory)
	var boundary *projectCandidate
	if len(projects) > 0 {
		if conflict := duplicateProjectConflict(projects); conflict != nil {
			return conflictResult(*conflict)
		}
		boundary = &projects[0]
	}
	var selectedRoot string
	var selectedScope contextapi.ScopeIdentity
	var selectedContributors map[contextapi.ContributorName]contextapi.Contributor
	var selectedProfile contextapi.ProfileSelection
	if boundary != nil {
		selectedRoot = boundary.snapshot.Scope.CanonicalRoot
		selectedScope = cloneScopeIdentity(boundary.snapshot.Scope)
		if !boundary.snapshot.Config.Enabled {
			reason := makeReason(contextapi.ReasonInactive, contextapi.ReasonConfigured, fmt.Sprintf("nearest project declaration %q is disabled", selectedRoot), input.Now)
			return inactiveResult(selectedScope, []contextapi.Reason{reason})
		}
		if !scopeCovers(boundary.snapshot.Config.Scope, selectedRoot, workingDirectory) {
			reason := makeReason(contextapi.ReasonInactive, contextapi.ReasonConfigured, fmt.Sprintf("nearest project declaration %q does not cover this directory", selectedRoot), input.Now)
			return inactiveResult(selectedScope, []contextapi.Reason{reason})
		}
		selectedContributors = cloneContributors(boundary.snapshot.Config.Contributors)
		selectedProfile = cloneProfileSelection(boundary.snapshot.Config.Profile)
	} else if len(homeMatches) > 0 {
		chosen := homeMatches[0].snapshot
		selectedRoot = chosen.Scope.CanonicalRoot
		selectedScope = cloneScopeIdentity(chosen.Scope)
		selectedContributors = cloneContributors(chosen.Config.Contributors)
		selectedProfile = cloneProfileSelection(chosen.Config.Profile)
	} else {
		reason := makeReason(contextapi.ReasonInactive, contextapi.ReasonConfigured, "no applicable project or home declaration", input.Now)
		return inactiveResult(contextapi.ScopeIdentity{}, []contextapi.Reason{reason})
	}

	providers, enabledCount := providersForContributors(selectedContributors, selectedRoot, input.Config.Home.Runtime.ProviderDefaults)
	if enabledCount == 0 {
		reason := makeReason(contextapi.ReasonProviderNotSelected, contextapi.ReasonConfigured, "declaration selects no enabled contributors", input.Now)
		return inactiveResult(selectedScope, []contextapi.Reason{reason})
	}
	filtered, blocked := applyProviderPolicy(providers, input.Config.Home.ProviderPolicy)
	reasons := make([]contextapi.Reason, 0, 1)
	if blocked {
		reasons = append(reasons, makeReason(contextapi.ReasonConflict, contextapi.ReasonConfigured, "home provider policy excludes one or more selected contributors", input.Now))
	}
	if len(filtered) == 0 {
		reasons = append(reasons, makeReason(contextapi.ReasonProviderNotSelected, contextapi.ReasonConfigured, "no selected contributor is permitted by the home policy", input.Now))
		return conflictResultWithScope(selectedScope, reasons)
	}
	runtime := resolveRuntimeLimits(input.Config.Home.Runtime)
	result := contextapi.ActivationResult{State: contextapi.ActivationEnabled, Scope: selectedScope, Effective: contextapi.EffectiveConfiguration{Scope: selectedScope, ConfigDigest: input.Config.ConfigDigest, Providers: cloneProviderConfigs(filtered), Profile: mergeProfile(input.Config.Home.Defaults.Selection, selectedProfile), Delivery: resolveEffectiveDelivery(input.Config.Home.Delivery, runtime), Runtime: runtime, Cache: resolveCacheLimits(input.Config.Home.Cache)}, Reasons: reasons}
	return result
}

func validateSnapshot(snapshot contextapi.ActivationSnapshot) []contextapi.Reason {
	reasons := make([]contextapi.Reason, 0)
	for _, project := range snapshot.Projects {
		if project.Scope.Authority != contextapi.ScopeAuthorityProject || !filepath.IsAbs(project.Scope.CanonicalRoot) || project.Scope.ConfigDigest == "" {
			reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("project declaration %q has invalid scope identity", project.SourcePath), time.Time{}))
			continue
		}
		if err := validateProjectDeclaration(project.Config, DefaultLoadLimits()); err != nil {
			reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("project declaration %q is invalid: %v", project.SourcePath, err), time.Time{}))
		}
	}
	for _, home := range snapshot.Home.Scopes {
		if home.Scope.Authority != contextapi.ScopeAuthorityHome || !filepath.IsAbs(home.Scope.CanonicalRoot) || home.Scope.ConfigDigest == "" {
			reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, "home scope has invalid scope identity", time.Time{}))
			continue
		}
		if err := validateDirectorySelection(home.Config, DefaultLoadLimits()); err != nil {
			reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("home scope %q is invalid: %v", home.Scope.CanonicalRoot, err), time.Time{}))
		}
	}
	if err := validateProviderPolicy(snapshot.Home.ProviderPolicy); err != nil {
		reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("home provider policy is invalid: %v", err), time.Time{}))
	}
	if err := validateRuntimeLimits(resolveRuntimeLimits(snapshot.Home.Runtime)); err != nil {
		reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("home runtime is invalid: %v", err), time.Time{}))
	}
	if err := validateCacheLimits(resolveCacheLimits(snapshot.Home.Cache)); err != nil {
		reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("home cache is invalid: %v", err), time.Time{}))
	}
	for _, exclusion := range snapshot.Home.Exclusions {
		if strings.TrimSpace(exclusion.Root) == "" || !filepath.IsAbs(exclusion.Root) {
			reasons = append(reasons, makeReason(contextapi.ReasonInvalidConfig, contextapi.ReasonConfigured, fmt.Sprintf("home exclusion %q has no absolute root", exclusion.Root), time.Time{}))
		}
	}
	return reasons
}

func validateProjectDeclaration(value contextapi.ProjectDeclaration, limits LoadLimits) error {
	if value.Scope != contextapi.DeclarationScopeDirectory && value.Scope != contextapi.DeclarationScopeSubtree {
		return fmt.Errorf("invalid declaration scope %q", value.Scope)
	}
	if len(value.Contributors) > limits.MaxContributors {
		return fmt.Errorf("contributor count exceeds %d", limits.MaxContributors)
	}
	return validateContributors(value.Contributors)
}

func validateHomeDeclaration(value contextapi.HomeDeclaration, limits LoadLimits) error {
	if len(value.Directories) > limits.MaxHomeScopes {
		return fmt.Errorf("directory selection count exceeds %d", limits.MaxHomeScopes)
	}
	if err := validateProviderPolicy(value.Limits.ProviderPolicy); err != nil {
		return err
	}
	if err := validateRuntimeLimits(value.Limits.Runtime); err != nil {
		return fmt.Errorf("runtime: %w", err)
	}
	if err := validateCacheLimits(value.Limits.Cache); err != nil {
		return fmt.Errorf("cache: %w", err)
	}
	if err := validateEngineLimits(value.Limits.Delivery); err != nil {
		return fmt.Errorf("delivery: %w", err)
	}
	for index, directory := range value.Directories {
		if err := validateDirectorySelection(directory, limits); err != nil {
			return fmt.Errorf("directory %d: %w", index, err)
		}
	}
	for index, exclusion := range value.Exclusions {
		if !filepath.IsAbs(exclusion.Root) || strings.TrimSpace(exclusion.Root) == "" {
			return fmt.Errorf("exclusion %d root must be absolute", index)
		}
	}
	return nil
}

func validateDirectorySelection(value contextapi.HomeDirectorySelection, limits LoadLimits) error {
	if !filepath.IsAbs(value.Root) || strings.TrimSpace(value.Root) == "" {
		return errors.New("root must be absolute")
	}
	if value.Scope != contextapi.DeclarationScopeDirectory && value.Scope != contextapi.DeclarationScopeSubtree {
		return fmt.Errorf("invalid declaration scope %q", value.Scope)
	}
	if len(value.Contributors) > limits.MaxContributors {
		return fmt.Errorf("contributor count exceeds %d", limits.MaxContributors)
	}
	return validateContributors(value.Contributors)
}

func validateContributors(values map[contextapi.ContributorName]contextapi.Contributor) error {
	for name, value := range values {
		if strings.TrimSpace(string(name)) == "" {
			return errors.New("contributor name is empty")
		}
		switch value.Kind {
		case contextapi.ContributorKindAiContext:
		case contextapi.ContributorKindExecutable:
			if value.Executable.Executable == "" {
				return fmt.Errorf("contributor %q has an empty executable path", name)
			}
			if len(value.Executable.Capabilities) == 0 {
				return fmt.Errorf("contributor %q has no capabilities", name)
			}
			seen := make(map[contextapi.ProviderCapability]struct{}, len(value.Executable.Capabilities))
			for _, capability := range value.Executable.Capabilities {
				if capability != contextapi.ProviderCapabilityProfile && capability != contextapi.ProviderCapabilityContribute {
					return fmt.Errorf("contributor %q has invalid capability %q", name, capability)
				}
				if _, exists := seen[capability]; exists {
					return fmt.Errorf("contributor %q repeats capability %q", name, capability)
				}
				seen[capability] = struct{}{}
			}
			if err := validateProviderLimits(value.Executable.Limits); err != nil {
				return fmt.Errorf("contributor %q: %w", name, err)
			}
		default:
			return fmt.Errorf("contributor %q has invalid kind %q", name, value.Kind)
		}
	}
	return nil
}

func validateProviderPolicy(policy contextapi.ProviderPolicy) error {
	switch policy.Mode {
	case "", contextapi.ProviderPolicyUnrestricted:
		if len(policy.Allowed) > 0 {
			return errors.New("unrestricted provider policy cannot contain an allowlist")
		}
	case contextapi.ProviderPolicyAllowlist:
		seen := make(map[contextapi.ProviderID]struct{}, len(policy.Allowed))
		for _, id := range policy.Allowed {
			if strings.TrimSpace(string(id)) == "" {
				return errors.New("provider policy allowlist contains an empty ID")
			}
			if _, exists := seen[id]; exists {
				return fmt.Errorf("provider policy allowlist duplicates %q", id)
			}
			seen[id] = struct{}{}
		}
	case contextapi.ProviderPolicyDenyAll:
		if len(policy.Allowed) > 0 {
			return errors.New("deny-all provider policy cannot contain an allowlist")
		}
	default:
		return fmt.Errorf("invalid provider policy mode %q", policy.Mode)
	}
	return nil
}

func matchingProjectBoundaries(projects []contextapi.ProjectSnapshot, directory string) []projectCandidate {
	matched := make([]projectCandidate, 0, len(projects))
	for _, project := range projects {
		if pathWithin(project.Scope.CanonicalRoot, directory) {
			matched = append(matched, projectCandidate{snapshot: cloneProjectSnapshot(project), depth: pathDepth(project.Scope.CanonicalRoot)})
		}
	}
	sort.SliceStable(matched, func(left, right int) bool {
		if matched[left].depth != matched[right].depth {
			return matched[left].depth > matched[right].depth
		}
		return matched[left].snapshot.SourcePath < matched[right].snapshot.SourcePath
	})
	return matched
}

func matchingHomeScopes(scopes []contextapi.HomeDirectorySnapshot, directory string) []homeCandidate {
	matched := make([]homeCandidate, 0, len(scopes))
	for _, scope := range scopes {
		if pathMatches(scope.Scope.CanonicalRoot, scope.Config.Scope, directory) {
			matched = append(matched, homeCandidate{snapshot: cloneHomeDirectorySnapshot(scope), depth: pathDepth(scope.Scope.CanonicalRoot)})
		}
	}
	sort.SliceStable(matched, func(left, right int) bool {
		if matched[left].depth != matched[right].depth {
			return matched[left].depth > matched[right].depth
		}
		return matched[left].snapshot.Scope.ID < matched[right].snapshot.Scope.ID
	})
	return matched
}

func duplicateProjectConflict(projects []projectCandidate) *contextapi.Reason {
	if len(projects) < 2 || projects[0].depth != projects[1].depth || projects[0].snapshot.Scope.CanonicalRoot != projects[1].snapshot.Scope.CanonicalRoot {
		return nil
	}
	reason := makeReason(contextapi.ReasonConflict, contextapi.ReasonConfigured, fmt.Sprintf("project declarations conflict at %q", projects[0].snapshot.Scope.CanonicalRoot), time.Time{})
	return &reason
}

func duplicateHomeConflict(scopes []homeCandidate) *contextapi.Reason {
	if len(scopes) < 2 || scopes[0].depth != scopes[1].depth || scopes[0].snapshot.Scope.CanonicalRoot != scopes[1].snapshot.Scope.CanonicalRoot {
		return nil
	}
	reason := makeReason(contextapi.ReasonConflict, contextapi.ReasonConfigured, fmt.Sprintf("home declarations conflict at %q", scopes[0].snapshot.Scope.CanonicalRoot), time.Time{})
	return &reason
}

func providersForContributors(values map[contextapi.ContributorName]contextapi.Contributor, root string, defaults contextapi.ProviderLimits) ([]contextapi.ProviderConfig, int) {
	names := make([]string, 0, len(values))
	byName := make(map[string]contextapi.Contributor, len(values))
	for name, value := range values {
		names = append(names, string(name))
		byName[string(name)] = value
	}
	sort.Strings(names)
	providers := make([]contextapi.ProviderConfig, 0, len(names))
	enabled := 0
	resolvedDefaults := providerLimitsWithDefaults(defaults)
	for _, rawName := range names {
		value := byName[rawName]
		if !value.Enabled {
			continue
		}
		enabled++
		if value.Kind == contextapi.ContributorKindAiContext {
			providers = append(providers, contextapi.ProviderConfig{ID: contextapi.ProviderID(rawName), Kind: contextapi.ProviderKindBuiltin, Capabilities: []contextapi.ProviderCapability{contextapi.ProviderCapabilityContribute}, Limits: resolvedDefaults})
			continue
		}
		limits := providerLimitsWithDefaultsFrom(value.Executable.Limits, resolvedDefaults)
		executable := value.Executable.Executable
		if !filepath.IsAbs(executable) {
			executable = filepath.Join(root, executable)
		}
		providers = append(providers, contextapi.ProviderConfig{ID: contextapi.ProviderID(rawName), Kind: contextapi.ProviderKindExecutable, Executable: filepath.Clean(executable), Arguments: cloneStrings(value.Executable.Arguments), Settings: append([]byte(nil), value.Executable.Settings...), Capabilities: cloneCapabilities(value.Executable.Capabilities), Limits: limits})
	}
	return providers, enabled
}

func applyProviderPolicy(providers []contextapi.ProviderConfig, policy contextapi.ProviderPolicy) ([]contextapi.ProviderConfig, bool) {
	if policy.Mode == "" || policy.Mode == contextapi.ProviderPolicyUnrestricted {
		return uniqueProviderConfigs(providers), false
	}
	if policy.Mode == contextapi.ProviderPolicyDenyAll {
		return nil, len(providers) > 0
	}
	allowed := make(map[contextapi.ProviderID]struct{}, len(policy.Allowed))
	for _, id := range policy.Allowed {
		allowed[id] = struct{}{}
	}
	filtered := make([]contextapi.ProviderConfig, 0, len(providers))
	blocked := false
	for _, provider := range providers {
		if _, ok := allowed[provider.ID]; !ok {
			blocked = true
			continue
		}
		filtered = append(filtered, cloneProviderConfig(provider))
	}
	return uniqueProviderConfigs(filtered), blocked
}

func pathWithin(root, directory string) bool {
	root, directory = filepath.Clean(root), filepath.Clean(directory)
	if root == directory {
		return true
	}
	relative, err := filepath.Rel(root, directory)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}

func pathMatches(root string, scope contextapi.DeclarationScope, directory string) bool {
	if !pathWithin(root, directory) {
		return false
	}
	return scope == contextapi.DeclarationScopeSubtree || filepath.Clean(root) == filepath.Clean(directory)
}

func scopeCovers(scope contextapi.DeclarationScope, root, directory string) bool {
	return pathMatches(root, scope, directory)
}

func pathDepth(path string) int {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	depth := 0
	for _, part := range strings.Split(strings.TrimPrefix(clean, volume), string(filepath.Separator)) {
		if part != "" && part != "." {
			depth++
		}
	}
	return depth
}

func homeScopeIdentity(scopes []homeCandidate) contextapi.ScopeIdentity {
	if len(scopes) == 0 {
		return contextapi.ScopeIdentity{}
	}
	return cloneScopeIdentity(scopes[0].snapshot.Scope)
}

func providerLimitsWithDefaults(input contextapi.ProviderLimits) contextapi.ProviderLimits {
	return providerLimitsWithDefaultsFrom(input, defaultProviderLimits())
}

func providerLimitsWithDefaultsFrom(input, defaults contextapi.ProviderLimits) contextapi.ProviderLimits {
	if defaults.DeadlineMs == 0 {
		defaults = defaultProviderLimits()
	}
	if input.DeadlineMs == 0 || input.DeadlineMs > defaults.DeadlineMs {
		input.DeadlineMs = defaults.DeadlineMs
	}
	if input.MaxResponseBytes == 0 || input.MaxResponseBytes > defaults.MaxResponseBytes {
		input.MaxResponseBytes = defaults.MaxResponseBytes
	}
	if input.MaxFacts == 0 || input.MaxFacts > defaults.MaxFacts {
		input.MaxFacts = defaults.MaxFacts
	}
	if input.MaxContributions == 0 || input.MaxContributions > defaults.MaxContributions {
		input.MaxContributions = defaults.MaxContributions
	}
	if input.MaxBodyBytes == 0 || input.MaxBodyBytes > defaults.MaxBodyBytes {
		input.MaxBodyBytes = defaults.MaxBodyBytes
	}
	return input
}

func defaultProviderLimits() contextapi.ProviderLimits {
	return contextapi.ProviderLimits{DeadlineMs: 500, MaxResponseBytes: 256 * 1024, MaxFacts: 32, MaxContributions: 64, MaxBodyBytes: 256 * 1024}
}

func defaultRuntimeLimits() contextapi.RuntimeLimits {
	return contextapi.RuntimeLimits{HookDeadlineMs: 2 * 1000, WholeHookDeadlineMs: 2 * 1000, MaxActivationAncestors: 64, MaxActivationConfigBytes: 256 * 1024, MaxActivationConfigFiles: 32, MaxProviders: 64, MaxProfileFacts: 32, MaxContributions: 64, MaxContributionBodyBytes: 256 * 1024, MaxActiveScopes: 64, MaxActiveAudiences: 256, MaxProviderProcesses: 4, MaxPendingMemoryBytes: 4 * 1024 * 1024, MaxPendingItems: 256, MaxOutstandingOffers: 128, MaxReceipts: 1024, ProviderDefaults: defaultProviderLimits(), IdleTTLMs: 5 * 60 * 1000}
}

func resolveRuntimeLimits(input contextapi.RuntimeLimits) contextapi.RuntimeLimits {
	defaults := defaultRuntimeLimits()
	if input.WholeHookDeadlineMs == 0 {
		input.WholeHookDeadlineMs = defaults.WholeHookDeadlineMs
	}
	if input.HookDeadlineMs == 0 {
		input.HookDeadlineMs = defaults.HookDeadlineMs
	}
	if input.MaxActivationAncestors == 0 {
		input.MaxActivationAncestors = defaults.MaxActivationAncestors
	}
	if input.MaxActivationConfigBytes == 0 {
		input.MaxActivationConfigBytes = defaults.MaxActivationConfigBytes
	}
	if input.MaxActivationConfigFiles == 0 {
		input.MaxActivationConfigFiles = defaults.MaxActivationConfigFiles
	}
	if input.MaxProviders == 0 {
		input.MaxProviders = defaults.MaxProviders
	}
	if input.MaxProfileFacts == 0 {
		input.MaxProfileFacts = defaults.MaxProfileFacts
	}
	if input.MaxContributions == 0 {
		input.MaxContributions = defaults.MaxContributions
	}
	if input.MaxContributionBodyBytes == 0 {
		input.MaxContributionBodyBytes = defaults.MaxContributionBodyBytes
	}
	if input.MaxActiveScopes == 0 {
		input.MaxActiveScopes = defaults.MaxActiveScopes
	}
	if input.MaxActiveAudiences == 0 {
		input.MaxActiveAudiences = defaults.MaxActiveAudiences
	}
	if input.MaxProviderProcesses == 0 {
		input.MaxProviderProcesses = defaults.MaxProviderProcesses
	}
	if input.MaxPendingMemoryBytes == 0 {
		input.MaxPendingMemoryBytes = defaults.MaxPendingMemoryBytes
	}
	if input.MaxPendingItems == 0 {
		input.MaxPendingItems = defaults.MaxPendingItems
	}
	if input.MaxOutstandingOffers == 0 {
		input.MaxOutstandingOffers = defaults.MaxOutstandingOffers
	}
	if input.MaxReceipts == 0 {
		input.MaxReceipts = defaults.MaxReceipts
	}
	if input.IdleTTLMs == 0 {
		input.IdleTTLMs = defaults.IdleTTLMs
	}
	input.ProviderDefaults = providerLimitsWithDefaultsFrom(input.ProviderDefaults, defaults.ProviderDefaults)
	return input
}

func defaultQueueLimits() contextapi.QueueLimits {
	return contextapi.QueueLimits{MaxItemBytes: 256 * 1024, MaxPendingBytes: 1024 * 1024, MaxPendingItems: 64, MaxAgeMs: 24 * 60 * 60 * 1000}
}

func defaultDeliveryLimits() contextapi.EngineLimits {
	return contextapi.EngineLimits{Queue: defaultQueueLimits(), MaxLiveOffers: 128, MaxReceipts: 1024, MaxOfferAgeMs: 24 * 60 * 60 * 1000, MaxRetainedBytes: defaultMaxRetainedBytes}
}

func resolveQueueLimits(input, defaults contextapi.QueueLimits) contextapi.QueueLimits {
	if input.MaxItemBytes == 0 {
		input.MaxItemBytes = defaults.MaxItemBytes
	}
	if input.MaxPendingBytes == 0 {
		input.MaxPendingBytes = defaults.MaxPendingBytes
	}
	if input.MaxPendingItems == 0 {
		input.MaxPendingItems = defaults.MaxPendingItems
	}
	if input.MaxAgeMs == 0 {
		input.MaxAgeMs = defaults.MaxAgeMs
	}
	return input
}

func resolveDeliveryLimits(input contextapi.EngineLimits, runtime contextapi.RuntimeLimits) contextapi.EngineLimits {
	defaults := defaultDeliveryLimits()
	runtime = resolveRuntimeLimits(runtime)
	input.Queue = resolveQueueLimits(input.Queue, defaults.Queue)
	if input.MaxLiveOffers == 0 {
		input.MaxLiveOffers = runtime.MaxOutstandingOffers
	}
	if input.MaxReceipts == 0 {
		input.MaxReceipts = runtime.MaxReceipts
	}
	if input.MaxOfferAgeMs == 0 {
		input.MaxOfferAgeMs = defaults.MaxOfferAgeMs
	}
	if input.MaxRetainedBytes == 0 {
		input.MaxRetainedBytes = defaults.MaxRetainedBytes
	}
	if input.MaxRetainedBytes > runtime.MaxPendingMemoryBytes {
		input.MaxRetainedBytes = runtime.MaxPendingMemoryBytes
	}
	return input
}

func resolveEffectiveDelivery(input contextapi.EngineLimits, runtime contextapi.RuntimeLimits) contextapi.EngineLimits {
	delivery := resolveDeliveryLimits(input, runtime)
	runtime = resolveRuntimeLimits(runtime)
	if delivery.MaxLiveOffers > runtime.MaxOutstandingOffers {
		delivery.MaxLiveOffers = runtime.MaxOutstandingOffers
	}
	if delivery.MaxReceipts > runtime.MaxReceipts {
		delivery.MaxReceipts = runtime.MaxReceipts
	}
	return delivery
}

func defaultCacheLimits() contextapi.CacheLimits {
	return contextapi.CacheLimits{DiskCapBytes: 5_000_000_000, MaxRecordBytes: 256 * 1024, MaxInputBytes: 256 * 1024, MaxStringBytes: 64 * 1024, MaxReasons: 64, MaxReasonParameters: 32, MaxQueryRecords: 256, MaxQueryBytes: 1024 * 1024, MaxScanBytes: 1024 * 1024, MaxBlockBytes: 1024 * 1024, MaxBlockRecords: 1024, MaxBlocks: 1024}
}

func resolveCacheLimits(input contextapi.CacheLimits) contextapi.CacheLimits {
	defaults := defaultCacheLimits()
	if input.DiskCapBytes == 0 {
		input.DiskCapBytes = defaults.DiskCapBytes
	}
	if input.MaxRecordBytes == 0 {
		input.MaxRecordBytes = defaults.MaxRecordBytes
	}
	if input.MaxInputBytes == 0 {
		input.MaxInputBytes = defaults.MaxInputBytes
	}
	if input.MaxStringBytes == 0 {
		input.MaxStringBytes = defaults.MaxStringBytes
	}
	if input.MaxReasons == 0 {
		input.MaxReasons = defaults.MaxReasons
	}
	if input.MaxReasonParameters == 0 {
		input.MaxReasonParameters = defaults.MaxReasonParameters
	}
	if input.MaxQueryRecords == 0 {
		input.MaxQueryRecords = defaults.MaxQueryRecords
	}
	if input.MaxQueryBytes == 0 {
		input.MaxQueryBytes = defaults.MaxQueryBytes
	}
	if input.MaxScanBytes == 0 {
		input.MaxScanBytes = defaults.MaxScanBytes
	}
	if input.MaxBlockBytes == 0 {
		input.MaxBlockBytes = defaults.MaxBlockBytes
	}
	if input.MaxBlockRecords == 0 {
		input.MaxBlockRecords = defaults.MaxBlockRecords
	}
	if input.MaxBlocks == 0 {
		input.MaxBlocks = defaults.MaxBlocks
	}
	return input
}

func validateRuntimeLimits(value contextapi.RuntimeLimits) error {
	if value.HookDeadlineMs > maxHookDeadlineMs || value.WholeHookDeadlineMs > maxWholeHookDeadlineMs {
		return errors.New("hook deadline exceeds configured maximum")
	}
	if value.HookDeadlineMs > value.WholeHookDeadlineMs {
		return errors.New("hook deadline cannot exceed whole-hook deadline")
	}
	if value.MaxActivationAncestors > maxActivationAncestors || value.MaxActivationConfigBytes > maxActivationConfigBytes || value.MaxActivationConfigFiles > maxActivationConfigFiles || value.MaxProviders > maxConfiguredProviders || value.MaxProfileFacts > maxConfiguredCount || value.MaxContributions > maxConfiguredCount || value.MaxContributionBodyBytes > maxContributionBodyBytes || value.MaxActiveScopes > maxConfiguredCount || value.MaxActiveAudiences > maxConfiguredCount || value.MaxProviderProcesses > maxConfiguredCount || value.MaxPendingItems > maxConfiguredCount || value.MaxOutstandingOffers > maxConfiguredCount || value.MaxReceipts > maxConfiguredCount || value.MaxPendingMemoryBytes > maxPendingMemoryBytes {
		return errors.New("runtime bound exceeds configured maximum")
	}
	if value.IdleTTLMs > maxCacheIdleTTLMs {
		return errors.New("idle TTL exceeds configured maximum")
	}
	return validateProviderLimits(value.ProviderDefaults)
}

func validateProviderLimits(value contextapi.ProviderLimits) error {
	if value.DeadlineMs > maxProviderDeadlineMs {
		return errors.New("provider deadline exceeds configured maximum")
	}
	if value.MaxResponseBytes > maxProviderBytes || value.MaxBodyBytes > maxProviderBytes {
		return errors.New("provider byte bound exceeds configured maximum")
	}
	if value.MaxFacts > maxConfiguredCount || value.MaxContributions > maxConfiguredCount {
		return errors.New("provider count exceeds configured maximum")
	}
	return nil
}

func validateQueueLimits(value contextapi.QueueLimits) error {
	if value.MaxItemBytes > maxQueueItemBytes || value.MaxPendingBytes > maxQueuePendingBytes {
		return errors.New("queue byte bound exceeds configured maximum")
	}
	if value.MaxPendingItems > maxConfiguredCount {
		return errors.New("queue item count exceeds configured maximum")
	}
	if value.MaxAgeMs > maxQueueAgeMs {
		return errors.New("queue age exceeds configured maximum")
	}
	return nil
}

func validateCacheLimits(value contextapi.CacheLimits) error {
	if value.DiskCapBytes > maxCacheDiskCapBytes || value.MaxRecordBytes > maxCacheBytes || value.MaxInputBytes > maxCacheBytes || value.MaxStringBytes > maxCacheStringBytes || value.MaxQueryBytes > maxCacheBytes || value.MaxScanBytes > maxCacheBytes || value.MaxBlockBytes > maxCacheBytes {
		return errors.New("cache bound exceeds configured maximum")
	}
	if value.MaxReasons > maxConfiguredCount || value.MaxReasonParameters > maxConfiguredCount || value.MaxQueryRecords > maxConfiguredCount || value.MaxBlockRecords > maxConfiguredCount || value.MaxBlocks > maxConfiguredCount {
		return errors.New("cache count exceeds configured maximum")
	}
	return nil
}

func validateEngineLimits(value contextapi.EngineLimits) error {
	if err := validateQueueLimits(value.Queue); err != nil {
		return err
	}
	if value.MaxLiveOffers > maxConfiguredCount || value.MaxReceipts > maxConfiguredCount {
		return errors.New("offer/receipt count exceeds configured maximum")
	}
	if value.MaxRetainedBytes > maxPendingMemoryBytes {
		return errors.New("delivery bound exceeds configured maximum")
	}
	if value.MaxOfferAgeMs > maxQueueAgeMs {
		return errors.New("offer age exceeds configured maximum")
	}
	return nil
}

func homeSnapshot(value contextapi.EvaluatedDeclaration) (contextapi.HomeSnapshot, error) {
	home := contextapi.HomeSnapshot{ConfigDigest: value.Revision, ProviderPolicy: cloneProviderPolicy(value.Home.Limits.ProviderPolicy), Defaults: cloneProfileDefaults(value.Home.Limits.Defaults), Delivery: resolveDeliveryLimits(value.Home.Limits.Delivery, value.Home.Limits.Runtime), Runtime: resolveRuntimeLimits(value.Home.Limits.Runtime), Cache: resolveCacheLimits(value.Home.Limits.Cache)}
	for _, selection := range value.Home.Directories {
		root, err := canonicalSelectionRoot(selection.Root)
		if err != nil {
			return contextapi.HomeSnapshot{}, err
		}
		selection.Root = root
		home.Scopes = append(home.Scopes, contextapi.HomeDirectorySnapshot{Scope: makeScopeIdentity(contextapi.ScopeAuthorityHome, root, value.Revision), Config: cloneHomeDirectorySelection(selection)})
	}
	for _, exclusion := range value.Home.Exclusions {
		root, err := canonicalSelectionRoot(exclusion.Root)
		if err != nil {
			return contextapi.HomeSnapshot{}, err
		}
		home.Exclusions = append(home.Exclusions, contextapi.PathRule{Root: root, IncludeChildren: exclusion.IncludeChildren})
	}
	return home, nil
}

func canonicalSelectionRoot(root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("root must be absolute")
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", errors.New("root is not a directory")
	}
	return filepath.Clean(canonical), nil
}

func makeScopeIdentity(authority contextapi.ScopeAuthority, root string, revision contextapi.ConfigDigest) contextapi.ScopeIdentity {
	return contextapi.ScopeIdentity{ID: contextapi.ScopeID("scope:" + digestString(string(authority)+"\x00"+filepath.Clean(root))), Authority: authority, CanonicalRoot: filepath.Clean(root), ConfigDigest: revision}
}

func activationDigest(home contextapi.HomeSnapshot, projects []contextapi.ProjectSnapshot, issues []loadIssue) contextapi.ConfigDigest {
	if len(issues) > 0 {
		return invalidDigestPrefix + "load"
	}
	parts := []string{"home:" + string(home.ConfigDigest)}
	for _, project := range projects {
		parts = append(parts, "project:"+project.SourcePath+":"+string(project.Scope.ConfigDigest))
	}
	sort.Strings(parts)
	return contextapi.ConfigDigest(sha256Prefix + digestString(strings.Join(parts, "\x00")))
}

func digestString(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func makeReason(code contextapi.ReasonCode, origin contextapi.ReasonOrigin, summary string, at time.Time) contextapi.Reason {
	return contextapi.Reason{Code: code, Origin: origin, Summary: summary, At: at}
}

func reasonsFromIssues(issues []loadIssue) []contextapi.Reason {
	reasons := make([]contextapi.Reason, 0, len(issues))
	for _, issue := range issues {
		reasons = append(reasons, issue.reason)
	}
	return reasons
}

func cancellationFromIssues(issues []loadIssue) error {
	for _, issue := range issues {
		if isCancellation(issue.err) {
			return issue.err
		}
	}
	return nil
}

func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func invalidLoadResult(input contextapi.ActivationInput, issues []loadIssue) LoadResult {
	return LoadResult{Input: input, Result: invalidResult(reasonsFromIssues(issues))}
}

func invalidResult(reasons []contextapi.Reason) contextapi.ActivationResult {
	return contextapi.ActivationResult{State: contextapi.ActivationInvalid, Reasons: cloneReasons(reasons)}
}

func inactiveResult(scope contextapi.ScopeIdentity, reasons []contextapi.Reason) contextapi.ActivationResult {
	return contextapi.ActivationResult{State: contextapi.ActivationInactive, Scope: cloneScopeIdentity(scope), Reasons: cloneReasons(reasons)}
}

func conflictResult(reason contextapi.Reason) contextapi.ActivationResult {
	return conflictResultWithScope(contextapi.ScopeIdentity{}, []contextapi.Reason{reason})
}

func conflictResultWithScope(scope contextapi.ScopeIdentity, reasons []contextapi.Reason) contextapi.ActivationResult {
	return contextapi.ActivationResult{State: contextapi.ActivationConflict, Scope: cloneScopeIdentity(scope), Reasons: cloneReasons(reasons)}
}

func mergeProfile(base, overlay contextapi.ProfileSelection) contextapi.ProfileSelection {
	base = cloneProfileSelection(base)
	if overlay.Role != "" {
		base.Role = overlay.Role
	}
	if overlay.GuidanceSet != "" {
		base.GuidanceSet = overlay.GuidanceSet
	}
	if overlay.Preferences != nil {
		merged := make([]contextapi.NamedValue, 0, len(base.Preferences)+len(overlay.Preferences))
		seen := make(map[string]struct{}, len(base.Preferences)+len(overlay.Preferences))
		for _, value := range overlay.Preferences {
			if _, exists := seen[value.Name]; !exists {
				merged = append(merged, cloneNamedValue(value))
				seen[value.Name] = struct{}{}
			}
		}
		for _, value := range base.Preferences {
			if _, exists := seen[value.Name]; !exists {
				merged = append(merged, cloneNamedValue(value))
				seen[value.Name] = struct{}{}
			}
		}
		base.Preferences = merged
	}
	return base
}

func cloneEvaluatedCapture(input EvaluatedDeclarationCapture) EvaluatedDeclarationCapture {
	return EvaluatedDeclarationCapture{Input: cloneEvaluationInput(input.Input), Declaration: cloneEvaluatedDeclaration(input.Declaration)}
}

func cloneEvaluatedDeclaration(input contextapi.EvaluatedDeclaration) contextapi.EvaluatedDeclaration {
	return contextapi.EvaluatedDeclaration{
		Origin:       contextapi.DeclarationOrigin{Path: strings.Clone(input.Origin.Path), Authority: input.Origin.Authority, Root: strings.Clone(input.Origin.Root)},
		Kind:         input.Kind,
		Project:      cloneProjectDeclaration(input.Project),
		Home:         cloneHomeDeclaration(input.Home),
		Dependencies: contextapi.CloneDependencyManifest(input.Dependencies),
		Revision:     contextapi.ConfigDigest(strings.Clone(string(input.Revision))),
		Evaluator:    contextapi.EvaluatorIdentity{Name: strings.Clone(input.Evaluator.Name), Version: strings.Clone(input.Evaluator.Version), Digest: contextapi.ContentID(strings.Clone(string(input.Evaluator.Digest)))},
	}
}

func cloneHomeDeclaration(input contextapi.HomeDeclaration) contextapi.HomeDeclaration {
	output := contextapi.HomeDeclaration{
		Limits:      contextapi.HomeLimits{ProviderPolicy: cloneProviderPolicy(input.Limits.ProviderPolicy), Defaults: cloneProfileDefaults(input.Limits.Defaults), Delivery: input.Limits.Delivery, Runtime: input.Limits.Runtime, Cache: input.Limits.Cache},
		Exclusions:  make([]contextapi.PathRule, len(input.Exclusions)),
		Directories: make([]contextapi.HomeDirectorySelection, len(input.Directories)),
	}
	for index, exclusion := range input.Exclusions {
		output.Exclusions[index] = contextapi.PathRule{Root: strings.Clone(exclusion.Root), IncludeChildren: exclusion.IncludeChildren}
	}
	for index, directory := range input.Directories {
		output.Directories[index] = cloneHomeDirectorySelection(directory)
	}
	return output
}

func cloneProjectDeclaration(input contextapi.ProjectDeclaration) contextapi.ProjectDeclaration {
	return contextapi.ProjectDeclaration{Enabled: input.Enabled, Scope: input.Scope, Contributors: cloneContributors(input.Contributors), Profile: cloneProfileSelection(input.Profile)}
}

func cloneHomeDirectorySelection(input contextapi.HomeDirectorySelection) contextapi.HomeDirectorySelection {
	return contextapi.HomeDirectorySelection{Root: strings.Clone(input.Root), Enabled: input.Enabled, Scope: input.Scope, Contributors: cloneContributors(input.Contributors), Profile: cloneProfileSelection(input.Profile)}
}

func cloneHomeDirectorySnapshot(input contextapi.HomeDirectorySnapshot) contextapi.HomeDirectorySnapshot {
	return contextapi.HomeDirectorySnapshot{Scope: cloneScopeIdentity(input.Scope), Config: cloneHomeDirectorySelection(input.Config)}
}

func cloneProjectSnapshot(input contextapi.ProjectSnapshot) contextapi.ProjectSnapshot {
	return contextapi.ProjectSnapshot{SourcePath: strings.Clone(input.SourcePath), Scope: cloneScopeIdentity(input.Scope), Config: cloneProjectDeclaration(input.Config)}
}

func cloneContributors(input map[contextapi.ContributorName]contextapi.Contributor) map[contextapi.ContributorName]contextapi.Contributor {
	if input == nil {
		return nil
	}
	output := make(map[contextapi.ContributorName]contextapi.Contributor, len(input))
	for name, value := range input {
		output[contextapi.ContributorName(strings.Clone(string(name)))] = cloneContributor(value)
	}
	return output
}

func cloneContributor(input contextapi.Contributor) contextapi.Contributor {
	output := input
	output.Executable.Executable = strings.Clone(input.Executable.Executable)
	output.Executable.Arguments = cloneStrings(input.Executable.Arguments)
	output.Executable.Capabilities = cloneCapabilities(input.Executable.Capabilities)
	output.Executable.Settings = append([]byte(nil), input.Executable.Settings...)
	return output
}

func cloneProfileDefaults(input contextapi.ProfileDefaults) contextapi.ProfileDefaults {
	return contextapi.ProfileDefaults{Selection: cloneProfileSelection(input.Selection)}
}

func cloneProfileSelection(input contextapi.ProfileSelection) contextapi.ProfileSelection {
	output := input
	if input.Preferences != nil {
		output.Preferences = make([]contextapi.NamedValue, len(input.Preferences))
		for index, value := range input.Preferences {
			output.Preferences[index] = cloneNamedValue(value)
		}
	}
	return output
}

func cloneNamedValue(input contextapi.NamedValue) contextapi.NamedValue {
	input.Name = strings.Clone(input.Name)
	input.Value.Text = strings.Clone(input.Value.Text)
	return input
}

func cloneProviderPolicy(input contextapi.ProviderPolicy) contextapi.ProviderPolicy {
	input.Allowed = cloneProviderIDs(input.Allowed)
	return input
}

func cloneProviderIDs(input []contextapi.ProviderID) []contextapi.ProviderID {
	if input == nil {
		return nil
	}
	output := make([]contextapi.ProviderID, len(input))
	for index, id := range input {
		output[index] = contextapi.ProviderID(strings.Clone(string(id)))
	}
	return output
}

func cloneScopeIdentity(input contextapi.ScopeIdentity) contextapi.ScopeIdentity {
	input.ID = contextapi.ScopeID(strings.Clone(string(input.ID)))
	input.CanonicalRoot = strings.Clone(input.CanonicalRoot)
	input.ConfigDigest = contextapi.ConfigDigest(strings.Clone(string(input.ConfigDigest)))
	return input
}

func cloneProviderConfig(input contextapi.ProviderConfig) contextapi.ProviderConfig {
	output := input
	output.ID = contextapi.ProviderID(strings.Clone(string(input.ID)))
	output.Executable = strings.Clone(input.Executable)
	output.Arguments = cloneStrings(input.Arguments)
	output.Capabilities = cloneCapabilities(input.Capabilities)
	output.Settings = append([]byte(nil), input.Settings...)
	return output
}

func cloneProviderConfigs(input []contextapi.ProviderConfig) []contextapi.ProviderConfig {
	if input == nil {
		return nil
	}
	output := make([]contextapi.ProviderConfig, len(input))
	for index, provider := range input {
		output[index] = cloneProviderConfig(provider)
	}
	return output
}

func cloneStrings(input []string) []string {
	if input == nil {
		return nil
	}
	output := make([]string, len(input))
	for index, value := range input {
		output[index] = strings.Clone(value)
	}
	return output
}

func cloneCapabilities(input []contextapi.ProviderCapability) []contextapi.ProviderCapability {
	if input == nil {
		return nil
	}
	output := make([]contextapi.ProviderCapability, len(input))
	for index, value := range input {
		output[index] = contextapi.ProviderCapability(strings.Clone(string(value)))
	}
	return output
}

func uniqueProviderConfigs(input []contextapi.ProviderConfig) []contextapi.ProviderConfig {
	output := make([]contextapi.ProviderConfig, 0, len(input))
	seen := make(map[contextapi.ProviderID]struct{}, len(input))
	for _, provider := range input {
		if _, exists := seen[provider.ID]; exists {
			continue
		}
		seen[provider.ID] = struct{}{}
		output = append(output, cloneProviderConfig(provider))
	}
	return output
}

func cloneReasons(input []contextapi.Reason) []contextapi.Reason {
	if input == nil {
		return nil
	}
	output := make([]contextapi.Reason, len(input))
	for index, reason := range input {
		output[index] = reason
		output[index].Summary = strings.Clone(reason.Summary)
		output[index].Rule = strings.Clone(reason.Rule)
		output[index].Params = append([]contextapi.ReasonParameter(nil), reason.Params...)
		output[index].Evidence = append([]contextapi.EvidenceID(nil), reason.Evidence...)
	}
	return output
}
