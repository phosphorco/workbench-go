package engine

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/phosphorco/workbench-go/internal/verify/declaration"
	"github.com/phosphorco/workbench-go/internal/verify/observe"
	"github.com/phosphorco/workbench-go/internal/verify/report"
	"github.com/phosphorco/workbench-go/internal/verify/sandbox"
	"github.com/phosphorco/workbench-go/internal/verify/selector"
	"github.com/phosphorco/workbench-go/internal/verify/store"
)

type runResult struct {
	outputs map[string]string
	stdout  string
	stderr  string
	scratch []store.DirectoryEntry
	unknown string
}

func (e *execution) stage(registered declaration.Registered, role, unit string, inputs, program []store.DirectoryEntry, upstream []store.Upstream, scratch []store.DirectoryEntry) (stage string, invocation sandbox.Invocation, err error) {
	parent := filepath.Join(e.home, "exec")
	if err = os.MkdirAll(parent, 0700); err != nil {
		return
	}
	stage, err = os.MkdirTemp(parent, "action-")
	if err != nil {
		return
	}
	defer func() {
		if err != nil {
			os.RemoveAll(stage)
		}
	}()
	m := registered.Manifest
	invocation = sandbox.Invocation{Image: m.Image, Command: m.Command, UnitID: unit, Role: role, Network: m.Network, Timeout: time.Duration(m.Timeout * float64(time.Second)), WorkDir: filepath.Join(stage, "work"), VerifierDir: filepath.Join(stage, "verifier"), OutputDir: filepath.Join(stage, "out")}
	if err = store.Materialize(e.home, inputs, invocation.WorkDir); err != nil {
		return
	}
	if err = store.Materialize(e.home, program, invocation.VerifierDir); err != nil {
		return
	}
	if err = os.Mkdir(invocation.OutputDir, 0755); err != nil {
		return
	}
	for _, up := range upstream {
		var entries []store.DirectoryEntry
		entries, err = store.ReadDirectory(e.home, up.Digest)
		if err != nil {
			return
		}
		host := filepath.Join(stage, "upstream", up.Name)
		if !filepath.IsLocal(up.Name) || strings.ContainsAny(up.Name, "/\\") {
			err = fmt.Errorf("invalid upstream mount name %q", up.Name)
			return
		}
		if err = store.Materialize(e.home, entries, host); err != nil {
			return
		}
		invocation.Upstream = append(invocation.Upstream, sandbox.Upstream{Name: up.Name, HostDir: host})
	}
	if scratch != nil {
		invocation.ScratchDir = filepath.Join(stage, "scratch")
		err = store.Materialize(e.home, scratch, invocation.ScratchDir)
	}
	return
}

func (e *execution) record(registered declaration.Registered, key, unit string, exit int, outputs map[string]string, out sandbox.Outcome) (store.IndexRecord, error) {
	stdout, err := store.Put(e.home, out.Stdout)
	if err != nil {
		return store.IndexRecord{}, err
	}
	stderr, err := store.Put(e.home, []byte(out.Stderr))
	if err != nil {
		return store.IndexRecord{}, err
	}
	return store.IndexRecord{Attestation: store.Attestation{ActionKey: key, Verifier: store.Verifier{ID: registered.Manifest.ID, Version: registered.Version}, UnitID: unit, Exit: exit, Outputs: outputs}, ComputedIn: e.tree.Hash, Stdout: stdout, Stderr: stderr}, nil
}

func (e *execution) failed(registered declaration.Registered, key, unit string, out sandbox.Outcome, runErr error) (string, error) {
	reason := ""
	if out.TimedOut {
		reason = fmt.Sprintf("timed out after %gs", registered.Manifest.Timeout)
	} else if out.ExitCode < 0 && runErr != nil {
		reason = runErr.Error()
	} else if out.ExitCode != 0 {
		reason = fmt.Sprintf("exit %d", out.ExitCode)
		lines := strings.Split(strings.TrimSpace(out.Stderr), "\n")
		if last := lines[len(lines)-1]; last != "" {
			reason += ": " + last
		}
	} else if runErr != nil {
		reason = runErr.Error()
	}
	if reason == "" {
		return "", nil
	}
	record, err := e.record(registered, key, unit, out.ExitCode, map[string]string{}, out)
	if err != nil {
		return "", err
	}
	if out.ExitCode != 0 && !out.TimedOut {
		_, err = store.Publish(e.home, e.tree.Hash, registered.Manifest.Deterministic, record)
		if errors.Is(err, store.ErrFailureNotAttested) {
			err = nil
		}
	} else {
		_, err = store.WriteWitness(e.home, record.Attestation.Verifier, store.WitnessRecord{Kind: "tool-failure", ActionKey: key, UnitID: unit, Exit: &out.ExitCode, Stdout: record.Stdout, Stderr: record.Stderr, Reason: reason})
	}
	return reason, err
}

func (e *execution) runUnit(ctx context.Context, registered declaration.Registered, unit string, inputs, program []store.DirectoryEntry, upstream []store.Upstream, scratch []store.DirectoryEntry, key string) (runResult, error) {
	stage, invocation, err := e.stage(registered, "run", unit, inputs, program, upstream, scratch)
	if err != nil {
		if errors.Is(err, store.ErrRefused) {
			return runResult{unknown: "output refused: " + err.Error()}, nil
		}
		return runResult{}, err
	}
	defer os.RemoveAll(stage)
	out, runErr := sandbox.Execute(ctx, invocation)
	reason, err := e.failed(registered, key, unit, out, runErr)
	if err != nil || reason != "" {
		return runResult{unknown: reason}, err
	}
	result := runResult{outputs: map[string]string{}, scratch: []store.DirectoryEntry{}}
	m := registered.Manifest
	if m.Produces == "report" {
		read, err := sandbox.ReadRegularOutput(invocation.OutputDir, "report.json")
		if err != nil {
			reason := "output refused: " + err.Error()
			if errors.Is(err, os.ErrNotExist) {
				reason = "no /out/report.json"
			}
			return runResult{unknown: reason}, nil
		}
		files := map[string]bool{}
		for _, input := range inputs {
			files[input.Path] = true
		}
		decoded := report.Decode(read.Bytes, report.Visibility{UnitID: unit, Tree: e.tree.Hash, Files: files})
		if decoded.Refused != "" {
			return runResult{unknown: "report refused: " + decoded.Refused}, nil
		}
		result.outputs["report.json"], err = store.Put(e.home, decoded.Bytes)
		if err != nil {
			return runResult{}, err
		}
	} else {
		entries, err := e.storeOutputs(out.Outputs, func(path string) bool {
			for _, output := range m.Outputs {
				if path == output || strings.HasPrefix(path, output+"/") {
					return true
				}
			}
			return false
		})
		if err != nil {
			return runResult{}, err
		}
		absent := []string{}
		for _, output := range m.Outputs {
			found := false
			for _, entry := range entries {
				if entry.Path == output || strings.HasPrefix(entry.Path, output+"/") {
					found = true
					break
				}
			}
			if !found {
				absent = append(absent, "/out/"+output)
			}
		}
		if len(absent) > 0 {
			return runResult{unknown: "missing output " + strings.Join(absent, ", ")}, nil
		}
		result.outputs["artifact"], err = store.PutDirectory(e.home, entries)
		if err != nil {
			return runResult{}, err
		}
	}
	if invocation.ScratchDir != "" {
		result.scratch, err = store.Collect(e.home, invocation.ScratchDir, func(path string) bool {
			for _, pattern := range m.Toolchain.Retained {
				if match, _ := doublestar.Match(pattern, path); match {
					return true
				}
			}
			return false
		})
		if err != nil {
			if errors.Is(err, store.ErrRefused) {
				return runResult{unknown: "output refused: " + err.Error()}, nil
			}
			return runResult{}, err
		}
	}
	result.stdout, err = store.Put(e.home, out.Stdout)
	if err != nil {
		return runResult{}, err
	}
	result.stderr, err = store.Put(e.home, []byte(out.Stderr))
	return result, err
}

func (e *execution) storeOutputs(outputs []sandbox.Output, keep func(string) bool) ([]store.DirectoryEntry, error) {
	entries := []store.DirectoryEntry{}
	for _, output := range outputs {
		if !keep(output.Path) {
			continue
		}
		digest, err := store.Put(e.home, output.Bytes)
		if err != nil {
			return nil, err
		}
		entries = append(entries, store.DirectoryEntry{Path: output.Path, Digest: digest, Mode: store.Mode(output.Mode)})
	}
	return entries, nil
}

func unknown(reason string) observe.Outcome {
	return observe.Outcome{Kind: observe.Indeterminate, Reason: reason}
}

func attested(record store.IndexRecord, won bool) observe.Outcome {
	a := record.Attestation
	attestation := observe.Attestation{ActionKey: a.ActionKey, Verifier: observe.VerifierVersion{ID: a.Verifier.ID, Version: a.Verifier.Version}, UnitID: a.UnitID, Exit: a.Exit, Outputs: a.Outputs}
	provenance := observe.Provenance{Recomputed: won}
	if !won {
		provenance.CarriedFrom = record.ComputedIn
	}
	return observe.Outcome{Kind: observe.Attested, Attestation: &attestation, Provenance: provenance}
}

func (e *execution) observeUnit(ctx context.Context, registered declaration.Registered, unit selector.Unit, program []store.DirectoryEntry, upstream []store.Upstream) (observe.Outcome, error) {
	inputs, err := e.inputs(unit.Inputs)
	if err != nil {
		return unknown(err.Error()), nil
	}
	key := store.ActionKey(action(registered, "run", unit.UnitID, inputs, program, upstream))
	found, err := store.Lookup(e.home, key, e.tree.Hash, registered.Manifest.Deterministic)
	if err != nil {
		return observe.Outcome{}, err
	}
	if found != nil {
		if registered.Manifest.Produces == "report" {
			digest := found.Attestation.Outputs["report.json"]
			read, readErr := sandbox.ReadRegularOutput(filepath.Join(e.home, "cas"), digest)
			if readErr != nil {
				return unknown("output refused: " + readErr.Error()), nil
			}
			files := map[string]bool{}
			for _, input := range inputs {
				files[input.Path] = true
			}
			decoded := report.Decode(read.Bytes, report.Visibility{UnitID: unit.UnitID, Tree: e.tree.Hash, Files: files})
			if decoded.Refused != "" {
				return unknown("report refused: " + decoded.Refused), nil
			}
		}
		return attested(*found, false), nil
	}
	v := store.Verifier{ID: registered.Manifest.ID, Version: registered.Version}
	weak := registered.Manifest.Toolchain.Kind == "weak"
	var restored *store.ScratchPointer
	var previous []store.DirectoryEntry
	if weak {
		restored, previous, err = store.RestoreScratch(e.home, v, unit.UnitID)
		if err != nil {
			return observe.Outcome{}, err
		}
		if previous == nil {
			previous = []store.DirectoryEntry{}
		}
	}
	warm, err := e.runUnit(ctx, registered, unit.UnitID, inputs, program, upstream, previous, key)
	if err != nil {
		return observe.Outcome{}, err
	}
	if warm.unknown != "" {
		return unknown(warm.unknown), nil
	}
	audit := ""
	if weak && restored != nil && e.sample(*registered.Manifest.Toolchain.AuditRate) {
		cold, err := e.runUnit(ctx, registered, unit.UnitID, inputs, program, upstream, []store.DirectoryEntry{}, key)
		if err != nil {
			return observe.Outcome{}, err
		}
		if cold.unknown != "" || !maps.Equal(warm.outputs, cold.outputs) {
			var coldEvidence any = cold.outputs
			if cold.unknown != "" {
				coldEvidence = map[string]string{"unknown": cold.unknown}
			}
			if _, err := store.WriteWitness(e.home, v, store.WitnessRecord{Kind: "scratch-audit", ActionKey: key, UnitID: unit.UnitID, Scratch: restored.Digest, Warm: warm.outputs, Cold: coldEvidence}); err != nil {
				return observe.Outcome{}, err
			}
			if err := store.DropScratch(e.home, v); err != nil {
				return observe.Outcome{}, err
			}
			return unknown("scratch audit mismatch"), nil
		}
		audit = "matched"
	}
	if weak {
		if err := store.SaveScratch(e.home, v, unit.UnitID, warm.scratch); err != nil {
			return observe.Outcome{}, err
		}
	}
	record := store.IndexRecord{Attestation: store.Attestation{ActionKey: key, Verifier: v, UnitID: unit.UnitID, Outputs: warm.outputs}, ComputedIn: e.tree.Hash, Stdout: warm.stdout, Stderr: warm.stderr}
	published, err := store.Publish(e.home, e.tree.Hash, registered.Manifest.Deterministic, record)
	if err != nil {
		return observe.Outcome{}, err
	}
	result := attested(published.Record, published.Won)
	if weak {
		result.Scratch = "absent"
		if restored != nil {
			result.Scratch = "overlaid"
		}
	}
	result.Audit = audit
	return result, nil
}

func (e *execution) sample(rate float64) bool {
	e.randomMu.Lock()
	defer e.randomMu.Unlock()
	return e.random() < rate
}
