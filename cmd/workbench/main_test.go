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
		{"version", []string{"version"}, call{name: "version"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []call
			err := runWith(context.Background(), test.arguments, func() (string, error) {
				calls = append(calls, call{name: "cwd"})
				return "/workbench", nil
			}, io.Discard, recordingApplications(&calls))
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
		{"prune"}, {"prune", ""}, {"version", "extra"},
	}
	for _, arguments := range invalid {
		var calls []call
		err := runWith(context.Background(), arguments, func() (string, error) {
			calls = append(calls, call{name: "cwd"})
			return "/workbench", nil
		}, io.Discard, recordingApplications(&calls))
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
	}, &output, application); err != nil {
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
			}, &output, application); err != nil {
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
		}, &output, application); err != nil {
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

func TestWorkingDirectoryFailurePrecedesApplication(t *testing.T) {
	t.Parallel()
	want := errors.New("unavailable")
	var calls []call
	err := runWith(context.Background(), []string{"setup"}, func() (string, error) { return "", want }, io.Discard, recordingApplications(&calls))
	if err == nil || !strings.Contains(err.Error(), want.Error()) {
		t.Fatalf("run error = %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("application called after cwd failure: %#v", calls)
	}
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
		version: func() (version.Info, error) {
			*calls = append(*calls, call{name: "version"})
			return version.Info{Release: "0.3.0", Revision: strings.Repeat("a", 40)}, nil
		},
	}
}
