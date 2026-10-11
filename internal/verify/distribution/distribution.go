// Package distribution promotes only the artifact attested for the current Tree.
package distribution

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/phosphorco/workbench-go/internal/verify/declaration"
	"github.com/phosphorco/workbench-go/internal/verify/observe"
	"github.com/phosphorco/workbench-go/internal/verify/store"
	"github.com/phosphorco/workbench-go/pkl"
)

const MacosReason = "no macOS pool exists yet; keep this as a Buildable"

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var platformPattern = regexp.MustCompile(`^[a-z0-9]+/[a-z0-9]+$`)

type Options struct {
	Home        string
	Root        string
	Tree        string
	Verifier    declaration.Registered
	Observation observe.Observation
}

type Receipt struct {
	Verifier       string `json:"verifier"`
	Tree           string `json:"tree"`
	Platform       string `json:"platform"`
	Target         string `json:"target"`
	ArtifactDigest string `json:"artifactDigest"`
}

// Validate refuses unsupported promotion declarations before any engine Run.
func Validate(verifier pkl.Verifier) error {
	if verifier.Distribution == nil {
		return fmt.Errorf("verifier %q has no distribution", verifier.ID)
	}
	if verifier.Produces != "artifact" {
		return errors.New("only artifact-producing verifiers may declare distribution")
	}
	d := verifier.Distribution
	if strings.HasPrefix(d.Platform, "darwin/") {
		return errors.New(MacosReason)
	}
	if !platformPattern.MatchString(d.Platform) {
		return fmt.Errorf("invalid distribution platform %q", d.Platform)
	}
	if !relative(d.Target) || slices.Contains(strings.Split(d.Target, "/"), ".git") {
		return fmt.Errorf("invalid promotion target %q", d.Target)
	}
	c := d.Manifest
	if c.SchemaVersion <= 0 || c.SchemaVersion > 65535 || c.Kind == "" || c.ContractID == "" {
		return errors.New("invalid manifest contract identity")
	}
	return nil
}

// Promote resolves the Observation's current action in the store, verifies all
// content digests and manifest facts, then replaces the target with staged bytes.
func Promote(options Options) (Receipt, error) {
	v := options.Verifier
	if err := Validate(v.Manifest); err != nil {
		return Receipt{}, err
	}
	if options.Tree == "" || options.Observation.Tree != options.Tree {
		return Receipt{}, errors.New("promotion requires an Observation of the current Tree")
	}
	for _, unknown := range options.Observation.Unknown {
		if unknown.Verifier == v.Manifest.ID {
			return Receipt{}, fmt.Errorf("unit %q unknown: %s", unknown.UnitID, unknown.Reason)
		}
	}
	var results []observe.Result
	for _, result := range options.Observation.Results {
		if result.Verifier == v.Manifest.ID {
			results = append(results, result)
		}
	}
	if len(results) != 1 {
		return Receipt{}, fmt.Errorf("artifact verifier %q has %d attested units; promotion requires exactly one", v.Manifest.ID, len(results))
	}
	result := results[0]
	if result.Attestation.Exit != 0 {
		return Receipt{}, fmt.Errorf("unit %q failing: exit %d", result.UnitID, result.Attestation.Exit)
	}
	if !digestPattern.MatchString(result.ActionKey) || result.Attestation.ActionKey != result.ActionKey || result.Attestation.Verifier.ID != v.Manifest.ID || result.Attestation.Verifier.Version != v.Version || result.Attestation.UnitID != result.UnitID {
		return Receipt{}, errors.New("invalid artifact attestation for current verifier")
	}
	record, err := store.Lookup(options.Home, result.ActionKey, options.Tree, v.Manifest.Deterministic)
	if err != nil {
		return Receipt{}, err
	}
	if record == nil {
		return Receipt{}, errors.New("artifact attestation is absent from the store")
	}
	a := record.Attestation
	if a.Exit != 0 {
		return Receipt{}, fmt.Errorf("unit %q failing: exit %d", a.UnitID, a.Exit)
	}
	if a.ActionKey != result.ActionKey || a.Verifier.ID != v.Manifest.ID || a.Verifier.Version != v.Version || a.UnitID != result.UnitID || !maps.Equal(a.Outputs, result.Attestation.Outputs) {
		return Receipt{}, errors.New("Observation does not match the stored attestation")
	}
	digest := a.Outputs["artifact"]
	directory, err := readBlob(options.Home, digest)
	if err != nil {
		return Receipt{}, err
	}
	var encoded struct {
		Directory [][3]string `json:"directory"`
	}
	if err := json.Unmarshal(directory, &encoded); err != nil {
		return Receipt{}, fmt.Errorf("artifact directory: %w", err)
	}
	entries := make([]store.DirectoryEntry, 0, len(encoded.Directory))
	blobs := map[string][]byte{}
	for _, tuple := range encoded.Directory {
		entry := store.DirectoryEntry{Path: tuple[0], Digest: tuple[1], Mode: store.Mode(tuple[2])}
		if !relative(entry.Path) {
			return Receipt{}, fmt.Errorf("invalid artifact path %q", entry.Path)
		}
		if _, exists := blobs[entry.Path]; exists {
			return Receipt{}, fmt.Errorf("duplicate artifact path %q", entry.Path)
		}
		if entry.Mode != store.Regular && entry.Mode != store.Executable && entry.Mode != store.Symlink {
			return Receipt{}, fmt.Errorf("invalid artifact mode for %q", entry.Path)
		}
		blob, err := readBlob(options.Home, entry.Digest)
		if err != nil {
			return Receipt{}, err
		}
		blobs[entry.Path] = blob
		entries = append(entries, entry)
	}
	if err := checkManifest(v.Manifest.Distribution, entries, blobs); err != nil {
		return Receipt{}, fmt.Errorf("manifest contract mismatch: %w", err)
	}
	if err := install(options.Home, options.Root, v.Manifest.Distribution.Target, digest, entries); err != nil {
		return Receipt{}, err
	}
	return Receipt{Verifier: v.Manifest.ID, Tree: options.Tree, Platform: v.Manifest.Distribution.Platform, Target: v.Manifest.Distribution.Target, ArtifactDigest: digest}, nil
}

func relative(value string) bool {
	for _, character := range value {
		if character <= 0x1f || character == 0x7f {
			return false
		}
	}
	return value != "" && value != "." && path.Clean(value) == value && !strings.HasPrefix(value, "/") && !strings.ContainsRune(value, '\\') && !slices.Contains(strings.Split(value, "/"), "..")
}

func readBlob(home, digest string) ([]byte, error) {
	if !digestPattern.MatchString(digest) {
		return nil, fmt.Errorf("invalid artifact digest %q", digest)
	}
	data, _, err := store.ReadRegularFile(store.BlobPath(home, digest))
	if err != nil {
		return nil, fmt.Errorf("read attested blob %s: %w", digest, err)
	}
	if store.SHA256(data) != digest {
		return nil, fmt.Errorf("attested blob %s has a digest mismatch", digest)
	}
	return data, nil
}

// This is the existing Buildable manifest wire shape; producer freshness is
// supplied by the current Tree's action attestation instead of a candidate seal.
type manifest struct {
	SchemaVersion       int               `json:"schemaVersion"`
	Kind                string            `json:"kind"`
	ContractID          string            `json:"contractId"`
	DeclarationIdentity string            `json:"declarationIdentity,omitempty"`
	Source              map[string]string `json:"source"`
	ProducerInputs      struct {
		Algorithm string `json:"algorithm"`
		Digest    string `json:"digest"`
	} `json:"producerInputs"`
	Capabilities []string `json:"capabilities"`
	Outputs      []output `json:"outputs"`
}

type output struct {
	Platform    string `json:"platform"`
	Path        string `json:"path"`
	Destination string `json:"destination,omitempty"`
	Kind        string `json:"kind,omitempty"`
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	Executable  bool   `json:"executable"`
}

func checkManifest(d *pkl.Distribution, entries []store.DirectoryEntry, blobs map[string][]byte) error {
	data, present := blobs["manifest.json"]
	if !present {
		return errors.New("artifact has no manifest.json")
	}
	byPath := map[string]store.DirectoryEntry{}
	for _, entry := range entries {
		byPath[entry.Path] = entry
	}
	if byPath["manifest.json"].Mode != store.Regular {
		return errors.New("manifest.json must be a regular file")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var m manifest
	if err := decoder.Decode(&m); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing manifest JSON")
	}
	c := d.Manifest
	if m.SchemaVersion != c.SchemaVersion || m.Kind != c.Kind || m.ContractID != c.ContractID {
		return fmt.Errorf("manifest contract identity is %d/%s/%s, want %d/%s/%s", m.SchemaVersion, m.Kind, m.ContractID, c.SchemaVersion, c.Kind, c.ContractID)
	}
	for field, expected := range c.ExpectedSource {
		if m.Source[field] != expected {
			return fmt.Errorf("manifest source field %q is %q, want %q", field, m.Source[field], expected)
		}
	}
	for _, field := range c.RequiredSourceFields {
		if m.Source[field] == "" {
			return fmt.Errorf("manifest source field %q is required", field)
		}
	}
	if m.ProducerInputs.Algorithm != "sha256" || !digestPattern.MatchString(m.ProducerInputs.Digest) {
		return errors.New("producer input digest is not lowercase SHA-256")
	}
	capabilities := map[string]bool{}
	for _, capability := range m.Capabilities {
		if capability == "" || capabilities[capability] {
			return errors.New("manifest capabilities are empty or duplicated")
		}
		capabilities[capability] = true
	}
	for _, required := range c.RequiredCapabilities {
		if !capabilities[required] {
			return fmt.Errorf("manifest lacks required capability %q", required)
		}
	}
	seen := map[string]bool{}
	for _, output := range m.Outputs {
		if output.Platform != d.Platform {
			return fmt.Errorf("manifest output %q has platform %q, want %q", output.Path, output.Platform, d.Platform)
		}
		entry, exists := byPath[output.Path]
		if !exists || output.Path == "manifest.json" || seen[output.Path] {
			return fmt.Errorf("manifest output %q is absent or repeated", output.Path)
		}
		seen[output.Path] = true
		if entry.Mode == store.Symlink || output.SHA256 != entry.Digest || output.Size != int64(len(blobs[output.Path])) || output.Size <= 0 || output.Executable != (entry.Mode == store.Executable) {
			return fmt.Errorf("manifest output facts for %q differ from attested entries", output.Path)
		}
		if output.Destination != "" && !relative(output.Destination) {
			return fmt.Errorf("invalid output destination %q", output.Destination)
		}
	}
	if len(seen) == 0 || len(seen) != len(entries)-1 {
		return errors.New("manifest outputs do not cover the artifact entries")
	}
	return nil
}

func install(home, repository, target, digest string, entries []store.DirectoryEntry) error {
	root, err := os.OpenRoot(repository)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(repository, ".verify-promote-")
	if err != nil {
		return err
	}
	keepBackup := false
	defer func() {
		if !keepBackup {
			_ = os.RemoveAll(staging)
		}
	}()
	if err := store.Materialize(home, entries, filepath.Join(staging, "artifact")); err != nil {
		return err
	}
	// Re-read staged bytes: no mutable CAS file can change the promoted content.
	actual, err := store.Collect(home, filepath.Join(staging, "artifact"), nil)
	if err != nil {
		return err
	}
	got, err := store.PutDirectory(home, actual)
	if err != nil {
		return err
	}
	if got != digest {
		return errors.New("staged artifact differs from attested digest")
	}
	name := filepath.Base(staging)
	backup := filepath.Join(name, "previous")
	hadTarget := false
	if _, err := root.Lstat(target); err == nil {
		if err := root.Rename(target, backup); err != nil {
			return err
		}
		hadTarget = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := root.Rename(filepath.Join(name, "artifact"), target); err != nil {
		if hadTarget {
			if restoreErr := root.Rename(backup, target); restoreErr != nil {
				keepBackup = true
				err = errors.Join(err, fmt.Errorf("restore target failed; previous bytes retained at %s: %w", filepath.Join(repository, backup), restoreErr))
			}
		}
		return err
	}
	return nil
}
