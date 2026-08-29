package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/orphan"
	"github.com/phosphorco/workbench-go/internal/setup"
	"github.com/phosphorco/workbench-go/internal/skills"
	"github.com/phosphorco/workbench-go/internal/version"
)

func TestRunWithDispatchesExactCommandsAndArguments(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		arguments []string
		want      call
	}{
		{"setup", []string{"setup"}, call{name: "setup", root: "/workbench"}},
		{"default commit", []string{"commit"}, call{name: "commit", root: "/workbench", values: []string{defaultCommitPlan}}},
		{"explicit commit", []string{"commit", "delivery.pkl"}, call{name: "commit", root: "/workbench", values: []string{"delivery.pkl"}}},
		{"default snapshot", []string{"snapshot", "record"}, call{name: "snapshot record", root: "/workbench", values: []string{defaultSnapshot}}},
		{"explicit snapshot", []string{"snapshot", "record", "exact.pkl"}, call{name: "snapshot record", root: "/workbench", values: []string{"exact.pkl"}}},
		{"reproduce", []string{"snapshot", "reproduce", "exact.pkl"}, call{name: "snapshot reproduce", root: "/workbench", values: []string{"exact.pkl"}}},
		{"prune", []string{"prune", "@scope", "owner/repository"}, call{name: "prune", root: "/workbench", values: []string{"@scope", "owner/repository"}}},
		{"run without arguments", []string{"run", "tsgo", "--"}, call{name: "run", root: "/workbench", values: []string{"tsgo"}}},
		{"run with arguments", []string{"run", "tsgo", "--", "-b", "."}, call{name: "run", root: "/workbench", values: []string{"tsgo", "-b", "."}}},
		{"skills check", []string{"skills", "check"}, call{name: "skills check", root: "/workbench"}},
		{"version", []string{"version"}, call{name: "version"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []call
			err := runWith(context.Background(), test.arguments, func() (string, error) {
				calls = append(calls, call{name: "cwd"})
				return "/workbench", nil
			}, io.Discard, io.Discard, recordingApplications(&calls))
			if err != nil {
				t.Fatal(err)
			}
			want := []call{test.want}
			if test.want.name != "version" {
				want = append([]call{{name: "cwd"}}, want...)
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %#v, want %#v", calls, want)
			}
		})
	}
}

func TestInvalidCommandsAcquireNoAuthority(t *testing.T) {
	t.Parallel()
	invalid := [][]string{
		nil, {"inspect"}, {"setup", "extra"}, {"commit", "a", "b"},
		{"snapshot"}, {"snapshot", "record", "a", "b"}, {"snapshot", "reproduce"},
		{"prune"}, {"prune", ""}, {"run"}, {"run", "tsgo"}, {"run", "", "--"}, {"run", "tsgo", "-b", "."},
		{"skills"}, {"skills", "check", "--root", "/tmp"}, {"skills", "list"}, {"version", "extra"},
	}
	for _, arguments := range invalid {
		var calls []call
		err := runWith(context.Background(), arguments, func() (string, error) {
			calls = append(calls, call{name: "cwd"})
			return "/workbench", nil
		}, io.Discard, io.Discard, recordingApplications(&calls))
		if err == nil || err.Error() != usage {
			t.Fatalf("run %q error = %v", arguments, err)
		}
		if len(calls) != 0 {
			t.Fatalf("invalid command acquired authority: %#v", calls)
		}
	}
}

func TestVersionDoesNotObserveWorkingDirectory(t *testing.T) {
	t.Parallel()
	application := recordingApplications(new([]call))
	application.version = func() (version.Info, error) {
		return version.Info{Release: "0.3.0", Revision: strings.Repeat("a", 40)}, nil
	}
	var output bytes.Buffer
	if err := runWith(context.Background(), []string{"version"}, func() (string, error) {
		t.Fatal("version observed cwd")
		return "", nil
	}, &output, io.Discard, application); err != nil {
		t.Fatal(err)
	}
	if got := output.String(); got != "workbench 0.3.0 ("+strings.Repeat("a", 40)+")\n" {
		t.Fatalf("version output = %q", got)
	}
}

func TestSetupReportsEveryOrphanThroughTheCommandSeam(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		result setup.Result
		want   string
	}{
		{
			name:   "no orphan preserves the successful summary",
			result: setup.Result{Resources: make([]setup.Resource, 2), ChangedPaths: []string{"package.json"}},
			want:   "Workbench reconciled 2 repositories; 1 generated path changed.\n",
		},
		{
			name: "one orphan exposes identity and canonical path only",
			result: setup.Result{
				Resources: make([]setup.Resource, 1),
				Orphans: []orphan.Candidate{{
					Identity: "phosphorco/library", GitHub: "secret-host/transport-name",
					CanonicalPath: "repos/library", Path: "/ambient/workbench/repos/library",
				}},
			},
			want: "Workbench reconciled 1 repository; 0 generated paths changed.\n" +
				"Orphaned checkout: phosphorco/library at repos/library\n",
		},
		{
			name: "multiple orphans are ordered by identity then canonical path",
			result: setup.Result{
				Resources: make([]setup.Resource, 1),
				Orphans: []orphan.Candidate{
					{Identity: "zeta/repository", CanonicalPath: "repos/zeta"},
					{Identity: "@alpha", CanonicalPath: "pkg/@alpha"},
					{Identity: "alpha/repository", CanonicalPath: "repos/alpha"},
				},
			},
			want: "Workbench reconciled 1 repository; 0 generated paths changed.\n" +
				"Orphaned checkout: @alpha at pkg/@alpha\n" +
				"Orphaned checkout: alpha/repository at repos/alpha\n" +
				"Orphaned checkout: zeta/repository at repos/zeta\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			application := recordingApplications(new([]call))
			application.setup = func(context.Context, string) (setup.Result, error) {
				return test.result, nil
			}
			var output bytes.Buffer
			if err := runWith(context.Background(), []string{"setup"}, func() (string, error) {
				return "/workbench", nil
			}, &output, io.Discard, application); err != nil {
				t.Fatal(err)
			}
			if got := output.String(); got != test.want {
				t.Fatalf("setup output = %q, want %q", got, test.want)
			}
		})
	}
}

func TestConvergentSetupRepeatsTheSameOrphanReportWithoutReorderingResult(t *testing.T) {
	t.Parallel()
	result := setup.Result{
		Resources: make([]setup.Resource, 1),
		Orphans: []orphan.Candidate{
			{Identity: "zeta/repository", CanonicalPath: "repos/zeta"},
			{Identity: "alpha/repository", CanonicalPath: "repos/alpha"},
		},
	}
	application := recordingApplications(new([]call))
	application.setup = func(context.Context, string) (setup.Result, error) { return result, nil }
	outputs := make([]string, 0, 2)
	for range 2 {
		var output bytes.Buffer
		if err := runWith(context.Background(), []string{"setup"}, func() (string, error) {
			return "/workbench", nil
		}, &output, io.Discard, application); err != nil {
			t.Fatal(err)
		}
		outputs = append(outputs, output.String())
	}
	if outputs[0] != outputs[1] {
		t.Fatalf("convergent setup reports differ: first %q second %q", outputs[0], outputs[1])
	}
	if got := result.Orphans[0].Identity; got != "zeta/repository" {
		t.Fatalf("setup reporting reordered application result: first identity = %q", got)
	}
}

func TestSetupRendersCatalogWarningsBeforeSuccessWithoutRecomputing(t *testing.T) {
	t.Parallel()
	warnings := []skills.Diagnostic{
		{Source: "phosphorco/entry", Path: "alpha/SKILL.md", Line: 7, Message: "first warning"},
		{Source: "phosphorco/library", Path: "beta/SKILL.md", Line: 11, Message: "second warning"},
	}
	application := recordingApplications(new([]call))
	application.setup = func(context.Context, string) (setup.Result, error) {
		return setup.Result{SkillWarnings: warnings}, nil
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runWith(context.Background(), []string{"setup"}, func() (string, error) {
		return "/workbench", nil
	}, &stdout, &stderr, application); err != nil {
		t.Fatal(err)
	}
	if got, want := stderr.String(), "phosphorco/entry:alpha/SKILL.md:7: warning: first warning\nphosphorco/library:beta/SKILL.md:11: warning: second warning\n"; got != want {
		t.Fatalf("setup warnings = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "Workbench reconciled 0 repositories; 0 generated paths changed.\n"; got != want {
		t.Fatalf("setup summary = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(warnings, []skills.Diagnostic{
		{Source: "phosphorco/entry", Path: "alpha/SKILL.md", Line: 7, Message: "first warning"},
		{Source: "phosphorco/library", Path: "beta/SKILL.md", Line: 11, Message: "second warning"},
	}) {
		t.Fatalf("setup warning carrier mutated: %#v", warnings)
	}
}

func TestWorkingDirectoryFailurePrecedesApplication(t *testing.T) {
	t.Parallel()
	want := errors.New("unavailable")
	var calls []call
	err := runWith(context.Background(), []string{"setup"}, func() (string, error) { return "", want }, io.Discard, io.Discard, recordingApplications(&calls))
	if err == nil || !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("run error = %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("application called after cwd failure: %#v", calls)
	}
}

func TestSkillsCheckRendersWarningsBeforeIssuesAndRepairAction(t *testing.T) {
	t.Parallel()
	application := recordingApplications(new([]call))
	application.skillsCheck = func(context.Context, string) (skills.Report, error) {
		return skills.Report{
			SkillCount:           2,
			CompositionEdgeCount: 1,
			Warnings:             []skills.Diagnostic{{Path: "zeta/SKILL.md", Line: 7, Message: "review this reference"}},
			Issues: []skills.Diagnostic{
				{Path: "alpha/SKILL.md", Line: 4, Message: "missing link target ../missing/SKILL.md"},
				{Path: "zeta/SKILL.md", Line: 7, Message: "composition link must name $alpha"},
			},
		}, nil
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := runWith(context.Background(), []string{"skills", "check"}, func() (string, error) {
		return "/workbench", nil
	}, &stdout, &stderr, application)
	if err == nil || err.Error() != "skills check: 2 skill contract violations" {
		t.Fatalf("skills check error = %v", err)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("invalid catalog wrote stdout = %q", got)
	}
	want := "zeta/SKILL.md:7: warning: review this reference\n" +
		"alpha/SKILL.md:4: missing link target ../missing/SKILL.md\n" +
		"zeta/SKILL.md:7: composition link must name $alpha\n" +
		"Fix the 2 listed skill contract violations, then rerun 'workbench skills check'.\n"
	if got := stderr.String(); got != want {
		t.Fatalf("skills diagnostics = %q, want %q", got, want)
	}
}

func TestSkillsCheckWarningsRemainNonblockingAndCounted(t *testing.T) {
	t.Parallel()
	application := recordingApplications(new([]call))
	application.skillsCheck = func(context.Context, string) (skills.Report, error) {
		return skills.Report{
			SkillCount:           2,
			CompositionEdgeCount: 1,
			Warnings:             []skills.Diagnostic{{Path: "planner/SKILL.md", Line: 8, Message: "cross-domain reference"}},
		}, nil
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if err := runWith(context.Background(), []string{"skills", "check"}, func() (string, error) {
		return "/workbench", nil
	}, &stdout, &stderr, application); err != nil {
		t.Fatal(err)
	}
	if got, want := stderr.String(), "planner/SKILL.md:8: warning: cross-domain reference\n"; got != want {
		t.Fatalf("skills warnings = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "2 skills · 1 composition edges · domain, link, and skill-reference contracts valid · 1 warning\n"; got != want {
		t.Fatalf("skills summary = %q, want %q", got, want)
	}
}

func TestSkillsCheckDoesNotMarkDiagnosticWriteFailureAsReported(t *testing.T) {
	t.Parallel()
	application := recordingApplications(new([]call))
	application.skillsCheck = func(context.Context, string) (skills.Report, error) {
		return skills.Report{Warnings: []skills.Diagnostic{{Path: "skill/SKILL.md", Message: "warning"}}}, nil
	}
	err := runWith(context.Background(), []string{"skills", "check"}, func() (string, error) {
		return "/workbench", nil
	}, io.Discard, rejectingWriter{}, application)
	if err == nil || !strings.Contains(err.Error(), "write skill warning") {
		t.Fatalf("diagnostic write error = %v", err)
	}
	var reported reportedError
	if errors.As(err, &reported) {
		t.Fatalf("operational write error was marked as already reported: %v", err)
	}
}

type rejectingWriter struct{}

func (rejectingWriter) Write([]byte) (int, error) {
	return 0, errors.New("write denied")
}

func TestBuildIdentitySelectsDevelopmentOrReleasedWithoutFallback(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		development bool
		want        string
	}{
		{"uninjected development", true, "development"},
		{"injected or malformed release", false, "released"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var factories []string
			chosen := chooseApplications(test.development,
				func() applications {
					factories = append(factories, "development")
					return applications{commit: func(context.Context, string, string) (string, error) { return "development", nil }}
				},
				func() applications {
					factories = append(factories, "released")
					return applications{commit: func(context.Context, string, string) (string, error) { return "released", nil }}
				},
			)
			if !reflect.DeepEqual(factories, []string{test.want}) {
				t.Fatalf("factories = %v, want only %s", factories, test.want)
			}
			report, err := chosen.commit(context.Background(), "/workbench", defaultCommitPlan)
			if err != nil || report != test.want {
				t.Fatalf("chosen application = %q, %v", report, err)
			}
		})
	}
}

type call struct {
	name   string
	root   string
	values []string
}

func recordingApplications(calls *[]call) applications {
	return applications{
		setup: func(_ context.Context, root string) (setup.Result, error) {
			*calls = append(*calls, call{name: "setup", root: root})
			return setup.Result{}, nil
		},
		commit: func(_ context.Context, root, plan string) (string, error) {
			*calls = append(*calls, call{name: "commit", root: root, values: []string{plan}})
			return "", nil
		},
		snapshotRecord: func(_ context.Context, root, output string) (string, error) {
			*calls = append(*calls, call{name: "snapshot record", root: root, values: []string{output}})
			return "", nil
		},
		snapshotReproduce: func(_ context.Context, root, input string) (string, error) {
			*calls = append(*calls, call{name: "snapshot reproduce", root: root, values: []string{input}})
			return "", nil
		},
		prune: func(_ context.Context, root string, identities []string) (string, error) {
			*calls = append(*calls, call{name: "prune", root: root, values: append([]string(nil), identities...)})
			return "", nil
		},
		runBuildable: func(_ context.Context, root, name string, arguments []string) error {
			values := append([]string{name}, arguments...)
			*calls = append(*calls, call{name: "run", root: root, values: values})
			return nil
		},
		skillsCheck: func(_ context.Context, root string) (skills.Report, error) {
			*calls = append(*calls, call{name: "skills check", root: root})
			return skills.Report{}, nil
		},
		version: func() (version.Info, error) {
			*calls = append(*calls, call{name: "version"})
			return version.Info{Release: "0.3.0", Revision: strings.Repeat("a", 40)}, nil
		},
	}
}
