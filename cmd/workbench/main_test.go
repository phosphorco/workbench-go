package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

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
		{"setup", []string{"setup"}, call{name: "setup", root: "/world"}},
		{"default commit", []string{"commit"}, call{name: "commit", root: "/world", values: []string{defaultCommitPlan}}},
		{"explicit commit", []string{"commit", "delivery.pkl"}, call{name: "commit", root: "/world", values: []string{"delivery.pkl"}}},
		{"default snapshot", []string{"snapshot", "record"}, call{name: "snapshot record", root: "/world", values: []string{defaultSnapshot}}},
		{"explicit snapshot", []string{"snapshot", "record", "exact.pkl"}, call{name: "snapshot record", root: "/world", values: []string{"exact.pkl"}}},
		{"reproduce", []string{"snapshot", "reproduce", "exact.pkl"}, call{name: "snapshot reproduce", root: "/world", values: []string{"exact.pkl"}}},
		{"prune", []string{"prune", "@scope", "owner/repository"}, call{name: "prune", root: "/world", values: []string{"@scope", "owner/repository"}}},
		{"version", []string{"version"}, call{name: "version"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls []call
			err := runWith(context.Background(), test.arguments, func() (string, error) {
				calls = append(calls, call{name: "cwd"})
				return "/world", nil
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
			return "/world", nil
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
			report, err := chosen.commit(context.Background(), "/world", defaultCommitPlan)
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
