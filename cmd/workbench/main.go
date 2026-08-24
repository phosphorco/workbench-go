package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/phosphorco/workbench-go/internal/setup"
	"github.com/phosphorco/workbench-go/internal/version"
)

const (
	defaultCommitPlan = "commit-plan.pkl"
	defaultSnapshot   = ".workbench/world-snapshot.pkl"
	usage             = "usage: workbench setup | commit [plan] | snapshot record [output] | snapshot reproduce <file> | prune <identity>... | version"
)

type setupApplication func(context.Context, string) (setup.Result, error)
type commitApplication func(context.Context, string, string) (string, error)
type snapshotRecordApplication func(context.Context, string, string) (string, error)
type snapshotReproduceApplication func(context.Context, string, string) (string, error)
type pruneApplication func(context.Context, string, []string) (string, error)
type versionApplication func() (version.Info, error)

// applications contains command-scoped capabilities. Parsing succeeds before
// runWith designates any one capability or observes the working directory.
type applications struct {
	setup             setupApplication
	commit            commitApplication
	snapshotRecord    snapshotRecordApplication
	snapshotReproduce snapshotReproduceApplication
	prune             pruneApplication
	version           versionApplication
}

type commandKind uint8

const (
	commandSetup commandKind = iota + 1
	commandCommit
	commandSnapshotRecord
	commandSnapshotReproduce
	commandPrune
	commandVersion
)

type invocation struct {
	kind      commandKind
	arguments []string
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getwd, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "workbench: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, workingDirectory func() (string, error), output io.Writer) error {
	application := chooseApplications(version.IsDevelopment(), developmentApplications, releasedApplications)
	return runWith(ctx, arguments, workingDirectory, output, application)
}

func chooseApplications(development bool, developmentFactory, releasedFactory func() applications) applications {
	if development {
		return developmentFactory()
	}
	return releasedFactory()
}

func runWith(ctx context.Context, arguments []string, workingDirectory func() (string, error), output io.Writer, application applications) error {
	command, err := parseInvocation(arguments)
	if err != nil {
		return err
	}
	if command.kind == commandVersion {
		if application.version == nil {
			return errors.New("version application is absent")
		}
		info, err := application.version()
		if err != nil {
			return fmt.Errorf("version: %w", err)
		}
		return writeReport(output, info.String())
	}
	root, err := workingDirectory()
	if err != nil {
		return fmt.Errorf("resolve current directory: %w", err)
	}
	switch command.kind {
	case commandSetup:
		if application.setup == nil {
			return errors.New("setup application is absent")
		}
		result, err := application.setup(ctx, root)
		if err != nil {
			return fmt.Errorf("setup: %w", err)
		}
		repositories := len(result.Resources)
		if repositories == 0 {
			repositories = len(result.World.Resources)
		}
		return writeReport(output, fmt.Sprintf("Workbench reconciled %d repositories; %d generated paths changed.", repositories, len(result.ChangedPaths)))
	case commandCommit:
		if application.commit == nil {
			return errors.New("commit application is absent")
		}
		report, err := application.commit(ctx, root, command.arguments[0])
		if err != nil {
			return fmt.Errorf("commit: %w", err)
		}
		return writeReport(output, report)
	case commandSnapshotRecord:
		if application.snapshotRecord == nil {
			return errors.New("snapshot record application is absent")
		}
		report, err := application.snapshotRecord(ctx, root, command.arguments[0])
		if err != nil {
			return fmt.Errorf("snapshot record: %w", err)
		}
		return writeReport(output, report)
	case commandSnapshotReproduce:
		if application.snapshotReproduce == nil {
			return errors.New("snapshot reproduce application is absent")
		}
		report, err := application.snapshotReproduce(ctx, root, command.arguments[0])
		if err != nil {
			return fmt.Errorf("snapshot reproduce: %w", err)
		}
		return writeReport(output, report)
	case commandPrune:
		if application.prune == nil {
			return errors.New("prune application is absent")
		}
		report, err := application.prune(ctx, root, append([]string(nil), command.arguments...))
		if err != nil {
			return fmt.Errorf("prune: %w", err)
		}
		return writeReport(output, report)
	default:
		return errors.New(usage)
	}
}

func parseInvocation(arguments []string) (invocation, error) {
	if len(arguments) == 0 {
		return invocation{}, errors.New(usage)
	}
	switch arguments[0] {
	case "setup":
		if len(arguments) == 1 {
			return invocation{kind: commandSetup}, nil
		}
	case "commit":
		if len(arguments) == 1 {
			return invocation{kind: commandCommit, arguments: []string{defaultCommitPlan}}, nil
		}
		if len(arguments) == 2 && arguments[1] != "" {
			return invocation{kind: commandCommit, arguments: []string{arguments[1]}}, nil
		}
	case "snapshot":
		if len(arguments) == 2 && arguments[1] == "record" {
			return invocation{kind: commandSnapshotRecord, arguments: []string{defaultSnapshot}}, nil
		}
		if len(arguments) == 3 && arguments[1] == "record" && arguments[2] != "" {
			return invocation{kind: commandSnapshotRecord, arguments: []string{arguments[2]}}, nil
		}
		if len(arguments) == 3 && arguments[1] == "reproduce" && arguments[2] != "" {
			return invocation{kind: commandSnapshotReproduce, arguments: []string{arguments[2]}}, nil
		}
	case "prune":
		if len(arguments) >= 2 {
			for _, identity := range arguments[1:] {
				if strings.TrimSpace(identity) == "" {
					return invocation{}, errors.New(usage)
				}
			}
			return invocation{kind: commandPrune, arguments: append([]string(nil), arguments[1:]...)}, nil
		}
	case "version":
		if len(arguments) == 1 {
			return invocation{kind: commandVersion}, nil
		}
	}
	return invocation{}, errors.New(usage)
}

func releasedApplications() applications {
	return applicationsForEnvironment(releasedEnvironment())
}

func developmentApplications() applications {
	return applicationsForEnvironment(developmentEnvironment())
}

func writeReport(output io.Writer, report string) error {
	if report == "" {
		return nil
	}
	if _, err := fmt.Fprintln(output, report); err != nil {
		return fmt.Errorf("write command report: %w", err)
	}
	return nil
}
