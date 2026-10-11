package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"

	"github.com/phosphorco/workbench-go/internal/verify/declaration"
	"github.com/phosphorco/workbench-go/internal/verify/observe"
	"github.com/phosphorco/workbench-go/internal/verify/selector"
	"github.com/phosphorco/workbench-go/internal/verify/store"
	"github.com/phosphorco/workbench-go/internal/verify/tree"
)

type Options struct {
	Home        string
	Tree        tree.Tree
	Declaration declaration.Declaration
	Concurrency int
	// Random is sampled only for an eligible restored-Scratch audit, serially.
	Random func() float64
}

type execution struct {
	home     string
	tree     tree.Tree
	entries  map[string]store.DirectoryEntry
	tracked  []string
	random   func() float64
	randomMu sync.Mutex
}

// Evaluate reads declaration, project, and lock bytes from the immutable Tree.
func Evaluate(ctx context.Context, pkl declaration.Pkl, snapshot tree.Tree, home string) (declaration.Declaration, error) {
	paths := []string{}
	for _, entry := range snapshot.Entries {
		if strings.HasSuffix(entry.Path, ".pkl") || entry.Path == "PklProject" || entry.Path == "PklProject.deps.json" {
			paths = append(paths, entry.Path)
		}
	}
	files, err := tree.Bytes(ctx, snapshot, paths)
	if err != nil {
		return declaration.Declaration{}, err
	}
	return declaration.Evaluate(ctx, pkl, files, home)
}

// Observe executes misses in requires order, bounds concurrent Units, and folds
// results and explicit unknowns. Caller-owned options and declarations are read-only.
func Observe(ctx context.Context, options Options) (observe.Observation, error) {
	ordered, err := declaration.Order(options.Declaration)
	if err != nil {
		return observe.Observation{}, err
	}
	concurrency := options.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}
	random := options.Random
	if random == nil {
		random = rand.Float64
	}
	e := &execution{home: options.Home, tree: options.Tree, entries: map[string]store.DirectoryEntry{}, tracked: []string{}, random: random}
	for _, entry := range options.Tree.Entries {
		e.tracked = append(e.tracked, entry.Path)
	}
	bytes, err := tree.Bytes(ctx, options.Tree, e.tracked)
	if err != nil {
		return observe.Observation{}, err
	}
	for _, entry := range options.Tree.Entries {
		digest, err := store.Put(e.home, bytes[entry.Path])
		if err != nil {
			return observe.Observation{}, err
		}
		e.entries[entry.Path] = store.DirectoryEntry{Path: entry.Path, Digest: digest, Mode: store.Mode(entry.Mode)}
	}
	metadata := []observe.Verifier{}
	selections := map[string]observe.Selection{}
	outcomes := map[observe.UnitKey]observe.Outcome{}
	artifacts := map[string]string{}
	for _, registered := range ordered {
		manifest := registered.Manifest
		v := observe.Verifier{ID: manifest.ID, Version: registered.Version, DisplayNoun: manifest.DisplayNoun, Produces: manifest.Produces}
		names := make([]string, 0, len(manifest.Requires))
		for name := range manifest.Requires {
			names = append(names, name)
		}
		slices.Sort(names)
		upstream := []store.Upstream{}
		blocked := []string{}
		for _, name := range names {
			id := manifest.Requires[name]
			v.Requires = append(v.Requires, observe.Requirement{Name: name, Verifier: id})
			if digest := artifacts[id]; digest != "" {
				upstream = append(upstream, store.Upstream{Name: name, Digest: digest})
			} else {
				blocked = append(blocked, "upstream "+id+" unknown")
			}
		}
		metadata = append(metadata, v)
		if len(blocked) > 0 {
			selections[manifest.ID] = observe.Selection{Unknown: strings.Join(blocked, "; ")}
			continue
		}
		program, err := e.inputs(manifest.Program)
		if err != nil {
			selections[manifest.ID] = observe.Selection{Unknown: err.Error()}
			continue
		}
		common := commonDirectory(manifest.Program)
		for i := range program {
			if common != "" {
				program[i].Path = strings.TrimPrefix(program[i].Path, common+"/")
			}
		}
		units, err := e.selectUnits(ctx, registered, program, upstream)
		if err != nil {
			var refused *selectionFailure
			if !errors.As(err, &refused) {
				return observe.Observation{}, err
			}
			selections[manifest.ID] = observe.Selection{Unknown: refused.Error()}
			continue
		}
		selection := observe.Selection{Units: []observe.Unit{}}
		for _, unit := range units {
			selection.Units = append(selection.Units, observe.Unit{UnitID: unit.UnitID, Inputs: unit.Inputs})
		}
		selections[manifest.ID] = selection
		observed, err := e.pool(ctx, registered, units, program, upstream, concurrency)
		if err != nil {
			return observe.Observation{}, err
		}
		for i, outcome := range observed {
			outcomes[observe.UnitKey{Verifier: manifest.ID, UnitID: units[i].UnitID}] = outcome
		}
		if manifest.Produces == "artifact" && len(observed) == 1 && observed[0].Kind == observe.Attested {
			artifacts[manifest.ID] = observed[0].Attestation.Outputs["artifact"]
		}
	}
	return observe.Fold(options.Tree.Hash, metadata, selections, outcomes), nil
}

func (e *execution) inputs(paths []string) ([]store.DirectoryEntry, error) {
	entries := make([]store.DirectoryEntry, 0, len(paths))
	for _, path := range paths {
		entry, exists := e.entries[path]
		if !exists {
			return nil, fmt.Errorf("%s is not tracked in tree %s", path, e.tree.Hash)
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func commonDirectory(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	common := strings.Split(paths[0], "/")
	common = common[:len(common)-1]
	for _, path := range paths[1:] {
		parts := strings.Split(path, "/")
		limit := min(len(common), len(parts)-1)
		i := 0
		for i < limit && common[i] == parts[i] {
			i++
		}
		common = common[:i]
	}
	return strings.Join(common, "/")
}

func action(registered declaration.Registered, role, unit string, inputs, program []store.DirectoryEntry, upstream []store.Upstream) store.Action {
	return store.Action{Verifier: registered.Manifest.ID, Version: registered.Version, Role: role, UnitID: unit, Inputs: inputs, Upstream: upstream, Program: program, Command: registered.Manifest.Command, ImageDigest: registered.ImageDigest}
}

func (e *execution) pool(ctx context.Context, registered declaration.Registered, units []selector.Unit, program []store.DirectoryEntry, upstream []store.Upstream, limit int) ([]observe.Outcome, error) {
	outcomes := make([]observe.Outcome, len(units))
	failures := make([]error, len(units))
	jobs := make(chan int, len(units))
	for i := range units {
		jobs <- i
	}
	close(jobs)
	var workers sync.WaitGroup
	for range min(limit, len(units)) {
		workers.Go(func() {
			for i := range jobs {
				outcomes[i], failures[i] = e.observeUnit(ctx, registered, units[i], program, upstream)
			}
		})
	}
	workers.Wait()
	for _, err := range failures {
		if err != nil {
			return nil, err
		}
	}
	return outcomes, nil
}
