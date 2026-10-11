package engine

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"

	"github.com/phosphorco/workbench-go/internal/verify/client"
	"github.com/phosphorco/workbench-go/internal/verify/observe"
	"github.com/phosphorco/workbench-go/internal/verify/report"
	"github.com/phosphorco/workbench-go/internal/verify/sandbox"
	"github.com/phosphorco/workbench-go/internal/verify/tree"
)

const Usage = `usage: workbench verify [file:<glob>]... [code:<c>,<c>]... [unit:<id>]... [member:<name>]...
                        [verifier:<id>]... [severity:<s>]... [focus:<name>]...
                        [--gate <name>] [--all] [--reset] [--json] [--home DIR] [--session ID]

Verifies tracked working-tree bytes against workbench.pkl and prints what changed
since this session last looked.

  --gate <name>  require the named Gate over the whole Observation
  --all          show all occurrences without writing Last Seen
  --reset        clear this session's Last Seen before presenting
  --json         print {observation, delta, focus}
  --home DIR     state directory (VERIFY_HOME, else ~/.verify)
  --session ID   session (PHOSPHOR_VERIFY_SESSION, BB_THREAD_ID, else default)

Exit 0: clean in focus and Gate satisfied; 1: blocking or Gate unsatisfied;
     2: usage, Tree, or declaration error; 3: unknown Unit in focus.
`

type IO struct {
	WorkingDirectory func() (string, error)
	Env              map[string]string
	Stdout           io.Writer
	Stderr           io.Writer
}

// Main presents before updating Last Seen; focus never narrows engine work.
func Main(ctx context.Context, argv []string, streams IO) (int, error) {
	options, err := client.Parse(argv, streams.Env)
	if err != nil {
		if _, writeErr := fmt.Fprintf(streams.Stderr, "%s\n%s", err, Usage); writeErr != nil {
			return 2, writeErr
		}
		return 2, nil
	}
	if options.Help {
		_, err := io.WriteString(streams.Stdout, Usage)
		return 0, err
	}
	fail := func(err error) (int, error) { _, writeErr := fmt.Fprintln(streams.Stderr, err); return 2, writeErr }
	cwd, err := streams.WorkingDirectory()
	if err != nil {
		return fail(err)
	}
	snapshot, err := tree.Compute(ctx, cwd, options.Home)
	if err != nil {
		return fail(err)
	}
	evaluated, err := Evaluate(ctx, snapshot, options.Home)
	if err != nil {
		return fail(err)
	}
	focuses := map[string]client.Focus{}
	for name, value := range evaluated.Repository.Focuses {
		focuses[name] = client.Focus(value)
	}
	focus, err := client.ResolveFocus(options, focuses)
	if err != nil {
		return fail(err)
	}
	var gate *client.Gate
	if options.Gate != nil {
		policy, exists := evaluated.Repository.Gates[*options.Gate]
		if !exists {
			return fail(fmt.Errorf("no gate %q in workbench.pkl", *options.Gate))
		}
		gate = &client.Gate{Requires: policy.Requires, Blocking: policy.Blocking}
	}
	concurrency, _ := strconv.Atoi(streams.Env["VERIFY_CONCURRENCY"])
	observation, err := Observe(ctx, Options{Home: options.Home, Tree: snapshot, Declaration: evaluated, Concurrency: concurrency})
	if err != nil {
		return fail(err)
	}
	units := []report.Unit{}
	paths := []string{}
	validResults := []observe.Result{}
	refused := func(result observe.Result, reason string) {
		observation.Unknown = append(observation.Unknown, observe.Unknown{Verifier: result.Verifier, UnitID: result.UnitID, Display: result.Display, Reason: reason})
	}
	for _, result := range observation.Results {
		digest := result.Attestation.Outputs["report.json"]
		if digest == "" {
			validResults = append(validResults, result)
			continue
		}
		read, err := sandbox.ReadRegularOutput(filepath.Join(options.Home, "cas"), digest)
		if err != nil {
			refused(result, "output refused: "+err.Error())
			continue
		}
		// Reports were validated for their exact input closure before publication.
		visibility := report.Visibility{UnitID: result.UnitID, Tree: snapshot.Hash, Files: map[string]bool{}}
		for _, entry := range snapshot.Entries {
			visibility.Files[entry.Path] = true
		}
		decoded := report.Decode(read.Bytes, visibility)
		if decoded.Refused != "" {
			refused(result, "report refused: "+decoded.Refused)
			continue
		}
		validResults = append(validResults, result)
		units = append(units, report.Unit{Verifier: result.Verifier, UnitID: result.UnitID, Report: *decoded.Report})
		for _, diagnostic := range decoded.Report.Diagnostics {
			if diagnostic.File != nil {
				paths = append(paths, *diagnostic.File)
			}
		}
	}
	observation.Results = validResults
	files, err := tree.Bytes(ctx, snapshot, paths)
	if err != nil {
		return fail(err)
	}
	occurrences, members := report.Occurrences(units, files), report.Members(units)
	var status *client.GateStatus
	var blocking []string
	if gate != nil {
		value := client.EvaluateGate(*options.Gate, *gate, observation, occurrences)
		status, blocking = &value, gate.Blocking
	}
	return client.Present(client.Presentation{Options: options, Observation: observation, Occurrences: occurrences, Members: members, Focus: focus, Gate: status, Blocking: blocking}, streams.Stdout)
}
