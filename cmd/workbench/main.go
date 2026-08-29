package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/phosphorco/workbench-go/internal/orphan"
	"github.com/phosphorco/workbench-go/internal/setup"
	"github.com/phosphorco/workbench-go/internal/skills"
	"github.com/phosphorco/workbench-go/internal/version"
)

const (
	defaultCommitPlan = "commit-plan.pkl"
	defaultSnapshot   = ".workbench/workbench-snapshot.pkl"
	usage             = "usage: workbench setup | commit [plan] | snapshot record [output] | snapshot reproduce <file> | prune <identity>... | run <buildable> -- <args> | skills check | version"
)

type setupApplication func(context.Context, string) (setup.Result, error)
type commitApplication func(context.Context, string, string) (string, error)
type snapshotRecordApplication func(context.Context, string, string) (string, error)
type snapshotReproduceApplication func(context.Context, string, string) (string, error)
type pruneApplication func(context.Context, string, []string) (string, error)
type runBuildableApplication func(context.Context, string, string, []string) error
type skillsCheckApplication func(context.Context, string) (skills.Report, error)
type versionApplication func() (version.Info, error)

// applications contains command-scoped capabilities. Parsing succeeds before
// runWith designates any one capability or observes the working directory.
type applications struct {
	setup             setupApplication
	commit            commitApplication
	snapshotRecord    snapshotRecordApplication
	snapshotReproduce snapshotReproduceApplication
	prune             pruneApplication
	runBuildable      runBuildableApplication
	skillsCheck       skillsCheckApplication
	version           versionApplication
}

type commandKind uint8

const (
	commandSetup commandKind = iota + 1
	commandCommit
	commandSnapshotRecord
	commandSnapshotReproduce
	commandPrune
	commandRunBuildable
	commandSkillsCheck
	commandVersion
)

type invocation struct {
	kind      commandKind
	arguments []string
}

// reportedError marks a subject failure whose complete actionable diagnostic
// has already reached stderr. Operational failures never use this type.
type reportedError struct {
	err error
}

func (failure reportedError) Error() string {
	return failure.err.Error()
}

func (failure reportedError) Unwrap() error {
	return failure.err
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Getwd, os.Stdout, os.Stderr); err != nil {
		var reported reportedError
		if !errors.As(err, &reported) {
			fmt.Fprintf(os.Stderr, "workbench: %v\n", err)
		}
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, workingDirectory func() (string, error), output, diagnostics io.Writer) error {
	application := chooseApplications(version.IsDevelopment(), developmentApplications, releasedApplications)
	return runWith(ctx, arguments, workingDirectory, output, diagnostics, application)
}

func chooseApplications(development bool, developmentFactory, releasedFactory func() applications) applications {
	if development {
		return developmentFactory()
	}
	return releasedFactory()
}

func runWith(ctx context.Context, arguments []string, workingDirectory func() (string, error), output, diagnostics io.Writer, application applications) error {
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
		if err := writeSkillWarnings(diagnostics, result.SkillWarnings); err != nil {
			return err
		}
		repositories := len(result.Resources)
		var report strings.Builder
		fmt.Fprintf(&report, "Workbench reconciled %d %s; %d generated %s changed.", repositories, plural(repositories, "repository", "repositories"), len(result.ChangedPaths), plural(len(result.ChangedPaths), "path", "paths"))
		orphans := slices.Clone(result.Orphans)
		slices.SortFunc(orphans, func(left, right orphan.Candidate) int {
			if order := strings.Compare(left.Identity, right.Identity); order != 0 {
				return order
			}
			return strings.Compare(left.CanonicalPath, right.CanonicalPath)
		})
		for _, candidate := range orphans {
			fmt.Fprintf(&report, "\nOrphaned checkout: %s at %s", candidate.Identity, candidate.CanonicalPath)
		}
		return writeReport(output, report.String())
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
	case commandRunBuildable:
		if application.runBuildable == nil {
			return errors.New("run buildable application is absent")
		}
		if err := application.runBuildable(ctx, root, command.arguments[0], append([]string(nil), command.arguments[1:]...)); err != nil {
			return fmt.Errorf("run %s: %w", command.arguments[0], err)
		}
		return nil
	case commandSkillsCheck:
		if application.skillsCheck == nil {
			return errors.New("skills check application is absent")
		}
		report, err := application.skillsCheck(ctx, root)
		if err != nil {
			return fmt.Errorf("skills check: %w", err)
		}
		if err := writeSkillDiagnostics(diagnostics, report); err != nil {
			return err
		}
		if len(report.Issues) > 0 {
			return reportedError{err: fmt.Errorf("skills check: %d skill contract %s", len(report.Issues), plural(len(report.Issues), "violation", "violations"))}
		}
		return writeReport(output, skillsCheckSummary(report))
	default:
		return errors.New(usage)
	}
}

func plural(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
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
	case "run":
		if len(arguments) >= 3 && arguments[1] != "" && arguments[2] == "--" {
			values := append([]string{arguments[1]}, arguments[3:]...)
			return invocation{kind: commandRunBuildable, arguments: values}, nil
		}
	case "skills":
		if len(arguments) == 2 && arguments[1] == "check" {
			return invocation{kind: commandSkillsCheck}, nil
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

func checkSkills(_ context.Context, root string) (skills.Report, error) {
	catalog, err := skills.Load([]skills.Source{{Root: filepath.Join(root, ".agents", "skills")}})
	if err != nil {
		return skills.Report{}, err
	}
	return catalog.Report(), nil
}

func writeSkillDiagnostics(output io.Writer, report skills.Report) error {
	if err := writeSkillWarnings(output, report.Warnings); err != nil {
		return err
	}
	for _, issue := range report.Issues {
		if _, err := fmt.Fprintf(output, "%s: %s\n", issue.Location(), issue.Message); err != nil {
			return fmt.Errorf("write skill issue: %w", err)
		}
	}
	if len(report.Issues) == 0 {
		return nil
	}
	if _, err := fmt.Fprintf(output, "Fix the %d listed skill contract %s, then rerun 'workbench skills check'.\n", len(report.Issues), plural(len(report.Issues), "violation", "violations")); err != nil {
		return fmt.Errorf("write skill repair action: %w", err)
	}
	return nil
}

func writeSkillWarnings(output io.Writer, warnings []skills.Diagnostic) error {
	for _, warning := range warnings {
		if _, err := fmt.Fprintf(output, "%s: warning: %s\n", warning.Location(), warning.Message); err != nil {
			return fmt.Errorf("write skill warning: %w", err)
		}
	}
	return nil
}

func skillsCheckSummary(report skills.Report) string {
	summary := fmt.Sprintf("%d skills · %d composition edges · domain, link, and skill-reference contracts valid", report.SkillCount, report.CompositionEdgeCount)
	if len(report.Warnings) > 0 {
		summary += fmt.Sprintf(" · %d %s", len(report.Warnings), plural(len(report.Warnings), "warning", "warnings"))
	}
	return summary
}
