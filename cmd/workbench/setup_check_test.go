package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/setup"
	"github.com/phosphorco/workbench-go/internal/workspace"
)

func TestSetupCheckReportsAndExitsOneOnDifferences(t *testing.T) {
	differing := setup.CheckReport{Owned: 30, Differing: []setup.FileDifference{}, Missing: []string{"a/package.json"}, Extra: []string{}, Notes: []workspace.Note{{Kind: "staleInclude", Detail: "x"}}}
	for index := 0; index < 25; index++ {
		differing.Differing = append(differing.Differing, setup.FileDifference{Path: "f", Diff: "--- a/f\n+++ b/f\n"})
	}
	for _, test := range []struct {
		arguments []string
		report    setup.CheckReport
		fail      bool
		want      string
	}{
		{[]string{"setup", "--check"}, setup.CheckReport{Owned: 3, Differing: []setup.FileDifference{}, Missing: []string{}, Extra: []string{}}, false, "all 3 owned files equal"},
		{[]string{"setup", "--check"}, differing, true, "5 more differing files omitted"},
		{[]string{"setup", "--check", "--json"}, differing, true, `"ownedFiles": 30`},
	} {
		var output bytes.Buffer
		err := runWith(context.Background(), test.arguments, func() (string, error) { return "/work", nil }, &output, &bytes.Buffer{}, applications{
			setupCheck: func(_ context.Context, root string) (setup.CheckReport, error) { return test.report, nil },
		})
		var reported reportedError
		if test.fail != (err != nil) || (err != nil && (!errors.As(err, &reported) || exitCode(err) != 1)) {
			t.Fatalf("%v: error = %v", test.arguments, err)
		}
		if !strings.Contains(output.String(), test.want) {
			t.Fatalf("%v output:\n%s", test.arguments, output.String())
		}
		if len(test.arguments) == 3 {
			var decoded setup.CheckReport
			if err := json.Unmarshal(output.Bytes(), &decoded); err != nil || len(decoded.Differing) != 25 {
				t.Fatalf("--json is capped or invalid: %d %v", len(decoded.Differing), err)
			}
		}
	}
	if _, err := parseInvocation([]string{"setup", "--json"}); err == nil {
		t.Fatal("setup --json without --check was admitted")
	}
}

func TestInPlaceCommitAdmitsOnlyOwnedFilesEqualToTheProjection(t *testing.T) {
	reject := inPlaceRejectPath(setup.CheckReport{
		Differing: []setup.FileDifference{{Path: "pkg/@a/b/package.json"}},
		Missing:   []string{"pkg/@a/b/tsconfig.json"},
		Extra:     []string{"pkg/@a/b/.tsconfigs/tsconfig.sources.child-index.json"},
	})
	for path, want := range map[string]bool{
		"pkg/@a/b/package.json": true, "./pkg/@a/b/tsconfig.json": true, "pkg/@a/b/.tsconfigs/tsconfig.sources.child-index.json": true,
		"pkg/@a/c/package.json": false, "tsconfig.json": false, "bun.lock": false, "pkg/@a/b/src/index.ts": false,
	} {
		if reject(path) != want {
			t.Errorf("reject(%q) = %v, want %v", path, !want, want)
		}
	}
	if generatedProjectionPath("pkg/@a/c/package.json") != true {
		t.Fatal("context commits must keep refusing generated manifests")
	}
}
