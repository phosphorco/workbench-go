package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/phosphorco/workbench-go/internal/verify/declaration"
	"github.com/phosphorco/workbench-go/internal/verify/observe"
	"github.com/phosphorco/workbench-go/internal/verify/sandbox"
	"github.com/phosphorco/workbench-go/internal/verify/selector"
	"github.com/phosphorco/workbench-go/internal/verify/store"
)

type selectionFailure struct{ reason string }

func (e *selectionFailure) Error() string { return e.reason }
func selectionRefused(err error) error    { return &selectionFailure{strings.Split(err.Error(), "\n")[0]} }

func (e *execution) decodeSelection(data []byte) ([]selector.Unit, error) {
	parent := filepath.Join(e.home, "exec")
	if err := os.MkdirAll(parent, 0700); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(parent, "selection-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "select.json"), data, 0600); err != nil {
		return nil, err
	}
	read, err := sandbox.ReadRegularOutput(dir, "select.json")
	if err != nil {
		return nil, selectionRefused(err)
	}
	units, err := selector.Decode(read.Bytes, e.tracked)
	if err != nil {
		return nil, selectionRefused(err)
	}
	return units, nil
}

func (e *execution) selectUnits(ctx context.Context, registered declaration.Registered, program []store.DirectoryEntry, upstream []store.Upstream) ([]selector.Unit, error) {
	m := registered.Manifest
	switch m.Selector.Kind {
	case "WholeTree":
		return selector.WholeTree(e.tracked, m.ID), nil
	case "Paths":
		units, err := selector.Paths(e.tracked, m.ID, m.Selector.Patterns)
		if err != nil {
			return nil, selectionRefused(err)
		}
		return units, nil
	case "Own":
		return e.selectOwn(ctx, registered, program, upstream)
	default:
		return nil, selectionRefused(fmt.Errorf("unknown selector kind %q", m.Selector.Kind))
	}
}

func (e *execution) selectOwn(ctx context.Context, registered declaration.Registered, program []store.DirectoryEntry, upstream []store.Upstream) ([]selector.Unit, error) {
	whole, err := e.inputs(e.tracked)
	if err != nil {
		return nil, err
	}
	key := store.ActionKey(action(registered, "select", observe.AllUnits, whole, program, upstream))
	found, err := store.Lookup(e.home, key, e.tree.Hash, registered.Manifest.Deterministic)
	if err != nil {
		return nil, err
	}
	if found == nil {
		stage, invocation, err := e.stage(registered, "select", observe.AllUnits, whole, program, upstream, nil)
		if err != nil {
			return nil, selectionRefused(err)
		}
		defer os.RemoveAll(stage)
		_, out, selectErr := selector.Own(ctx, e.tracked, invocation)
		reason, err := e.failed(registered, key, observe.AllUnits, out, selectErr)
		if err != nil {
			return nil, err
		}
		if reason != "" {
			return nil, selectionRefused(fmt.Errorf("select %s", reason))
		}
		if _, err := e.decodeSelection(out.Stdout); err != nil {
			return nil, err
		}
		digest, err := store.Put(e.home, out.Stdout)
		if err != nil {
			return nil, err
		}
		record, err := e.record(registered, key, observe.AllUnits, 0, map[string]string{"select.json": digest}, out)
		if err != nil {
			return nil, err
		}
		published, err := store.Publish(e.home, e.tree.Hash, registered.Manifest.Deterministic, record)
		if err != nil {
			return nil, err
		}
		found = &published.Record
	}
	digest := found.Attestation.Outputs["select.json"]
	if digest == "" {
		return nil, selectionRefused(fmt.Errorf("select attestation has no select.json"))
	}
	data, err := store.Read(e.home, digest)
	if err != nil {
		return nil, err
	}
	return e.decodeSelection(data)
}
