package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const ownershipManifestRelativePath = ".agents/skills/.workbench-owned.json"
const ownershipManifestVersion = 1

// Projection is an opaque, write-free projection plan. Callers projecting
// several destinations should plan all of them and pass the complete set to
// ApplyPlans so every known predicate is refreshed before the first write.
type Projection struct {
	root           string
	projectionRoot string
	desired        map[string]Skill
	previous       ownershipManifest
	previousBytes  []byte
	manifestExists bool
	actual         map[string]observedTree
	tracks         TrackedPathObserver
	noOp           bool
	valid          bool
}

// TrackedPathObserver reports whether the slash-normalized relative path or
// any descendant is Git-tracked, including tracked paths missing from disk.
type TrackedPathObserver func(relativePath string) (bool, error)

// Plan is only for destinations outside Git authority, such as isolated unit
// test roots. Git worktrees must use PlanWithTracking.
func Plan(root string, selected []Skill) (Projection, error) {
	return plan(root, selected, nil)
}

func PlanWithTracking(root string, selected []Skill, tracks TrackedPathObserver) (Projection, error) {
	if tracks == nil {
		return Projection{}, fmt.Errorf("tracked-path observer is required for a Git destination")
	}
	return plan(root, selected, tracks)
}

func plan(root string, selected []Skill, tracks TrackedPathObserver) (Projection, error) {
	desired, err := desiredSkills(selected)
	if err != nil {
		return Projection{}, err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return Projection{}, fmt.Errorf("resolve projected skill destination: %w", err)
	}
	projectionRoot := filepath.Join(root, ".agents", "skills")
	if err := preflightProjectedLinks(projectionRoot, desired); err != nil {
		return Projection{}, err
	}
	if err := preflightProjectionParents(root); err != nil {
		return Projection{}, err
	}
	if err := rejectTrackedProjectionPaths(tracks, ownershipManifestRelativePath); err != nil {
		return Projection{}, err
	}

	previous, previousBytes, manifestExists, err := readOwnershipManifest(root)
	if err != nil {
		return Projection{}, err
	}
	if !manifestExists && len(desired) == 0 {
		return Projection{root: root, projectionRoot: projectionRoot, desired: desired, tracks: tracks, noOp: true, valid: true}, nil
	}
	trackedPaths := make([]string, 0, len(previous.Skills)+len(desired))
	for _, owned := range previous.Skills {
		trackedPaths = append(trackedPaths, filepath.ToSlash(filepath.Join(".agents", "skills", owned.Name)))
	}
	for name := range desired {
		trackedPaths = append(trackedPaths, filepath.ToSlash(filepath.Join(".agents", "skills", name)))
	}
	if err := rejectTrackedProjectionPaths(tracks, trackedPaths...); err != nil {
		return Projection{}, err
	}

	actual := make(map[string]observedTree, len(previous.Skills))
	for _, owned := range previous.Skills {
		observed, err := observeTree(filepath.Join(projectionRoot, owned.Name))
		if err != nil {
			return Projection{}, fmt.Errorf("observe Workbench-owned skill %q: %w", owned.Name, err)
		}
		if observed.Exists && observed.Digest != owned.Digest {
			return Projection{}, fmt.Errorf("Workbench-owned skill %q no longer matches its recorded digest; refusing to replace or remove it", owned.Name)
		}
		actual[owned.Name] = observed
	}

	ownedByName := previous.byName()
	for name := range desired {
		if _, owned := ownedByName[name]; owned {
			continue
		}
		if _, err := os.Lstat(filepath.Join(projectionRoot, name)); err == nil {
			return Projection{}, fmt.Errorf("selected skill %q collides with a foreign projection; refusing to mutate it", name)
		} else if !os.IsNotExist(err) {
			return Projection{}, fmt.Errorf("inspect selected skill projection %q: %w", name, err)
		}
	}
	return Projection{
		root:           root,
		projectionRoot: projectionRoot,
		desired:        desired,
		previous:       previous,
		previousBytes:  previousBytes,
		manifestExists: manifestExists,
		actual:         actual,
		tracks:         tracks,
		valid:          true,
	}, nil
}

func Apply(root string, selected []Skill) ([]string, error) {
	projection, err := Plan(root, selected)
	if err != nil {
		return nil, err
	}
	return ApplyPlan(projection)
}

func ApplyPlan(projection Projection) ([]string, error) {
	results, err := ApplyPlans([]Projection{projection})
	if err != nil {
		return nil, err
	}
	return results[0], nil
}

// ApplyPlans refreshes every plan's filesystem predicates before mutating any
// destination. It cannot make independent filesystems transactional, but a
// known late collision or ownership drift never follows an earlier write.
func ApplyPlans(projections []Projection) ([][]string, error) {
	seenRoots := make(map[string]struct{}, len(projections))
	for _, projection := range projections {
		if _, duplicate := seenRoots[projection.projectionRoot]; duplicate {
			return nil, fmt.Errorf("projected skill destination %q is planned more than once", projection.root)
		}
		seenRoots[projection.projectionRoot] = struct{}{}
		if err := revalidateProjection(projection); err != nil {
			return nil, err
		}
	}
	results := make([][]string, 0, len(projections))
	for _, projection := range projections {
		changed, err := applyProjection(projection)
		if err != nil {
			return nil, err
		}
		results = append(results, changed)
	}
	return results, nil
}

func revalidateProjection(projection Projection) error {
	if !projection.valid {
		return fmt.Errorf("invalid projected skill plan")
	}
	if err := preflightProjectionParents(projection.root); err != nil {
		return err
	}
	trackedPaths := []string{ownershipManifestRelativePath}
	for _, owned := range projection.previous.Skills {
		trackedPaths = append(trackedPaths, filepath.ToSlash(filepath.Join(".agents", "skills", owned.Name)))
	}
	for name := range projection.desired {
		trackedPaths = append(trackedPaths, filepath.ToSlash(filepath.Join(".agents", "skills", name)))
	}
	if err := rejectTrackedProjectionPaths(projection.tracks, trackedPaths...); err != nil {
		return err
	}
	_, previousBytes, manifestExists, err := readOwnershipManifest(projection.root)
	if err != nil {
		return err
	}
	if manifestExists != projection.manifestExists || !bytes.Equal(previousBytes, projection.previousBytes) {
		return fmt.Errorf("projected skill ownership changed after planning at %q", projection.root)
	}
	if projection.noOp {
		return nil
	}
	if err := preflightProjectedLinks(projection.projectionRoot, projection.desired); err != nil {
		return fmt.Errorf("revalidate projected skill links: %w", err)
	}
	for _, owned := range projection.previous.Skills {
		observed, err := observeTree(filepath.Join(projection.projectionRoot, owned.Name))
		if err != nil {
			return fmt.Errorf("re-observe Workbench-owned skill %q: %w", owned.Name, err)
		}
		if observed != projection.actual[owned.Name] {
			return fmt.Errorf("Workbench-owned skill %q changed after planning; refusing the projection batch", owned.Name)
		}
	}
	ownedByName := projection.previous.byName()
	for name := range projection.desired {
		if _, owned := ownedByName[name]; owned {
			continue
		}
		if _, err := os.Lstat(filepath.Join(projection.projectionRoot, name)); err == nil {
			return fmt.Errorf("selected skill %q gained a foreign projection after planning; refusing the projection batch", name)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("re-inspect selected skill projection %q: %w", name, err)
		}
	}
	return nil
}

func rejectTrackedProjectionPaths(tracks TrackedPathObserver, relativePaths ...string) error {
	if tracks == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(relativePaths))
	for _, relative := range relativePaths {
		relative = filepath.ToSlash(relative)
		if _, duplicate := seen[relative]; duplicate {
			continue
		}
		seen[relative] = struct{}{}
		tracked, err := tracks(relative)
		if err != nil {
			return fmt.Errorf("observe Git ownership at %q: %w", relative, err)
		}
		if tracked {
			return fmt.Errorf("projected skill path %q is Git-tracked; refusing to mutate it", relative)
		}
	}
	return nil
}

func applyProjection(projection Projection) ([]string, error) {
	if projection.noOp {
		return nil, nil
	}
	projectionRoot := projection.projectionRoot
	desired := projection.desired
	previous := projection.previous
	previousBytes := projection.previousBytes
	manifestExists := projection.manifestExists
	actual := projection.actual
	ownedByName := previous.byName()

	if err := os.MkdirAll(projectionRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create projected skill root: %w", err)
	}
	changed := make([]string, 0)
	for _, owned := range previous.Skills {
		if _, remainsSelected := desired[owned.Name]; remainsSelected {
			continue
		}
		if actual[owned.Name].Exists {
			if err := os.RemoveAll(filepath.Join(projectionRoot, owned.Name)); err != nil {
				return nil, fmt.Errorf("remove stale Workbench-owned skill %q: %w", owned.Name, err)
			}
			changed = append(changed, filepath.ToSlash(filepath.Join(".agents", "skills", owned.Name)))
		}
	}

	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		skill := desired[name]
		digest := digestFiles(skill.Files)
		if owned, wasOwned := ownedByName[name]; wasOwned && actual[name].Exists && owned.Digest == digest {
			continue
		}
		if err := writeSkillTree(projectionRoot, skill); err != nil {
			return nil, err
		}
		paths := make([]string, 0, len(skill.Files))
		for relative := range skill.Files {
			paths = append(paths, filepath.ToSlash(filepath.Join(".agents", "skills", name, relative)))
		}
		sort.Strings(paths)
		changed = append(changed, paths...)
	}

	next := newOwnershipManifest(desired)
	nextBytes, err := encodeOwnershipManifest(next)
	if err != nil {
		return nil, err
	}
	if !manifestExists || !bytes.Equal(previousBytes, nextBytes) {
		if err := writeFileAtomically(filepath.Join(projection.root, ownershipManifestRelativePath), nextBytes); err != nil {
			return nil, fmt.Errorf("write projected skill ownership manifest: %w", err)
		}
		changed = append(changed, ownershipManifestRelativePath)
	}
	sort.Strings(changed)
	return changed, nil
}

type ownedSkill struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type ownershipManifest struct {
	Version int          `json:"version"`
	Skills  []ownedSkill `json:"skills"`
}

func (manifest ownershipManifest) byName() map[string]ownedSkill {
	result := make(map[string]ownedSkill, len(manifest.Skills))
	for _, skill := range manifest.Skills {
		result[skill.Name] = skill
	}
	return result
}

type observedTree struct {
	Exists bool
	Digest string
}

func desiredSkills(selected []Skill) (map[string]Skill, error) {
	desired := make(map[string]Skill, len(selected))
	for _, skill := range selected {
		if !skillNamePattern.MatchString(skill.Name) {
			return nil, fmt.Errorf("invalid skill name %q", skill.Name)
		}
		if len(skill.Files) == 0 {
			return nil, fmt.Errorf("skill %q contains no files", skill.Name)
		}
		for relative := range skill.Files {
			if err := validateSkillFilePath(relative); err != nil {
				return nil, fmt.Errorf("skill %q contains escaping path %q", skill.Name, relative)
			}
		}
		if previous, exists := desired[skill.Name]; exists {
			if !equalSkill(previous, skill) {
				return nil, fmt.Errorf("selected skill %q has conflicting definitions", skill.Name)
			}
			continue
		}
		files := make(map[string][]byte, len(skill.Files))
		for relative, contents := range skill.Files {
			files[relative] = bytes.Clone(contents)
		}
		desired[skill.Name] = Skill{
			Name:         skill.Name,
			Description:  skill.Description,
			Domain:       skill.Domain,
			Dependencies: append([]string(nil), skill.Dependencies...),
			Links:        append([]LocalLink(nil), skill.Links...),
			Files:        files,
		}
	}
	return desired, nil
}

func preflightProjectedLinks(projectionRoot string, desired map[string]Skill) error {
	expected := make(map[string]struct{})
	for name, skill := range desired {
		for relative := range skill.Files {
			expected[filepath.Clean(filepath.Join(projectionRoot, name, relative))] = struct{}{}
		}
	}
	for _, name := range sortedKeys(desired) {
		skill := desired[name]
		for _, link := range skill.Links {
			document := filepath.Join(projectionRoot, name, filepath.FromSlash(link.DocumentPath))
			target := filepath.Clean(filepath.Join(filepath.Dir(document), filepath.FromSlash(link.Target)))
			if _, projected := expected[target]; projected {
				continue
			}
			if _, err := os.Stat(target); err == nil {
				continue
			} else if !os.IsNotExist(err) {
				return fmt.Errorf("inspect projected link target %q: %w", target, err)
			}
			location := filepath.ToSlash(filepath.Join(name, link.DocumentPath))
			if link.Source != "" {
				location = link.Source + ":" + location
			}
			if link.Line > 0 {
				location = fmt.Sprintf("%s:%d", location, link.Line)
			}
			return fmt.Errorf("%s: projected link target %q would be absent at %q; keep linked material inside the skill, select the peer skill, or use an external link because Workbench copies skill bytes without rewriting authored paths", location, link.Target, target)
		}
	}
	return nil
}

func validateSkillFilePath(relative string) error {
	if relative == "" || relative == "." || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || strings.Contains(relative, `\`) {
		return fmt.Errorf("invalid relative path")
	}
	return nil
}

func preflightProjectionParents(root string) error {
	for _, path := range []string{filepath.Join(root, ".agents"), filepath.Join(root, ".agents", "skills")} {
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect projected skill parent %q: %w", path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("projected skill parent %q is not a real directory", path)
		}
	}
	return nil
}

func readOwnershipManifest(root string) (ownershipManifest, []byte, bool, error) {
	path := filepath.Join(root, ownershipManifestRelativePath)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return ownershipManifest{Version: ownershipManifestVersion}, nil, false, nil
	}
	if err != nil {
		return ownershipManifest{}, nil, false, fmt.Errorf("inspect projected skill ownership manifest: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ownershipManifest{}, nil, false, fmt.Errorf("projected skill ownership manifest is not a regular file")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return ownershipManifest{}, nil, false, fmt.Errorf("read projected skill ownership manifest: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(contents))
	decoder.DisallowUnknownFields()
	var manifest ownershipManifest
	if err := decoder.Decode(&manifest); err != nil {
		return ownershipManifest{}, nil, false, fmt.Errorf("decode projected skill ownership manifest: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return ownershipManifest{}, nil, false, fmt.Errorf("decode projected skill ownership manifest: trailing data")
	}
	if manifest.Version != ownershipManifestVersion {
		return ownershipManifest{}, nil, false, fmt.Errorf("unsupported projected skill ownership manifest version %d", manifest.Version)
	}
	seen := make(map[string]struct{}, len(manifest.Skills))
	for _, skill := range manifest.Skills {
		if !skillNamePattern.MatchString(skill.Name) {
			return ownershipManifest{}, nil, false, fmt.Errorf("projected skill ownership manifest contains invalid name %q", skill.Name)
		}
		if !validDigest(skill.Digest) {
			return ownershipManifest{}, nil, false, fmt.Errorf("projected skill ownership manifest contains invalid digest for %q", skill.Name)
		}
		if _, duplicate := seen[skill.Name]; duplicate {
			return ownershipManifest{}, nil, false, fmt.Errorf("projected skill ownership manifest repeats %q", skill.Name)
		}
		seen[skill.Name] = struct{}{}
	}
	canonical, err := encodeOwnershipManifest(manifest)
	if err != nil {
		return ownershipManifest{}, nil, false, err
	}
	if !bytes.Equal(contents, canonical) {
		return ownershipManifest{}, nil, false, fmt.Errorf("projected skill ownership manifest is not canonical")
	}
	return manifest, contents, true, nil
}

func newOwnershipManifest(desired map[string]Skill) ownershipManifest {
	names := make([]string, 0, len(desired))
	for name := range desired {
		names = append(names, name)
	}
	sort.Strings(names)
	owned := make([]ownedSkill, 0, len(names))
	for _, name := range names {
		owned = append(owned, ownedSkill{Name: name, Digest: digestFiles(desired[name].Files)})
	}
	return ownershipManifest{Version: ownershipManifestVersion, Skills: owned}
}

func encodeOwnershipManifest(manifest ownershipManifest) ([]byte, error) {
	contents, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode projected skill ownership manifest: %w", err)
	}
	return append(contents, '\n'), nil
}

func validDigest(digest string) bool {
	if len(digest) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(digest, "sha256:") {
		return false
	}
	for _, character := range digest[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func observeTree(root string) (observedTree, error) {
	info, err := os.Lstat(root)
	if os.IsNotExist(err) {
		return observedTree{}, nil
	}
	if err != nil {
		return observedTree{}, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return observedTree{}, fmt.Errorf("path is not a real directory")
	}
	files := make(map[string][]byte)
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("contains symlink %q", path)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("contains non-regular file %q", path)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[relative] = contents
		return nil
	})
	if err != nil {
		return observedTree{}, err
	}
	return observedTree{Exists: true, Digest: digestFiles(files)}, nil
}

func digestFiles(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, filepath.ToSlash(path))
	}
	sort.Strings(paths)
	hash := sha256.New()
	var size [8]byte
	for _, path := range paths {
		contents := files[filepath.FromSlash(path)]
		binary.BigEndian.PutUint64(size[:], uint64(len(path)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write([]byte(path))
		binary.BigEndian.PutUint64(size[:], uint64(len(contents)))
		_, _ = hash.Write(size[:])
		_, _ = hash.Write(contents)
	}
	return fmt.Sprintf("sha256:%x", hash.Sum(nil))
}

func writeSkillTree(projectionRoot string, skill Skill) error {
	stage, err := os.MkdirTemp(projectionRoot, ".workbench-stage-")
	if err != nil {
		return fmt.Errorf("stage projected skill %q: %w", skill.Name, err)
	}
	defer func() { _ = os.RemoveAll(stage) }()
	stagedTree := filepath.Join(stage, skill.Name)
	paths := make([]string, 0, len(skill.Files))
	for relative := range skill.Files {
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		target := filepath.Join(stagedTree, relative)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fmt.Errorf("create staged skill parent %q: %w", relative, err)
		}
		if err := os.WriteFile(target, skill.Files[relative], 0o644); err != nil {
			return fmt.Errorf("write staged skill path %q: %w", relative, err)
		}
	}
	target := filepath.Join(projectionRoot, skill.Name)
	previous := filepath.Join(stage, ".workbench-previous")
	if _, err := os.Lstat(target); err == nil {
		if err := os.Rename(target, previous); err != nil {
			return fmt.Errorf("stage previous projected skill %q: %w", skill.Name, err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect projected skill %q: %w", skill.Name, err)
	}
	if err := os.Rename(stagedTree, target); err != nil {
		if _, previousErr := os.Lstat(previous); previousErr == nil {
			_ = os.Rename(previous, target)
		}
		return fmt.Errorf("install projected skill %q: %w", skill.Name, err)
	}
	return nil
}

func writeFileAtomically(target string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(target), ".workbench-manifest-")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, target)
}

func equalSkill(left Skill, right Skill) bool {
	if left.Name != right.Name || left.Domain != right.Domain || !equalStrings(left.Dependencies, right.Dependencies) || len(left.Files) != len(right.Files) {
		return false
	}
	for path, contents := range left.Files {
		if !bytes.Equal(contents, right.Files[path]) {
			return false
		}
	}
	return true
}

func equalStrings(left []string, right []string) bool {
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
