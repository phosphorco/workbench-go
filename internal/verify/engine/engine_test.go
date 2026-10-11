package engine_test

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/phosphorco/workbench-go/internal/verify/declaration"
	"github.com/phosphorco/workbench-go/internal/verify/engine"
	"github.com/phosphorco/workbench-go/internal/verify/observe"
	"github.com/phosphorco/workbench-go/internal/verify/store"
	"github.com/phosphorco/workbench-go/internal/verify/tree"
	"github.com/phosphorco/workbench-go/pkl"
)

const image = "alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8"
const emptyReport = `printf '{"unitId":"%s","diagnostics":[]}' "$VERIFY_UNIT" > /out/report.json`

func write(t *testing.T, root, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, path), []byte(text), 0644); err != nil {
		t.Fatal(err)
	}
}
func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %s %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}
func verifier(id string, selector pkl.Selector) pkl.Verifier {
	return pkl.Verifier{ID: id, Image: image, Program: []string{"verifiers/" + id + "/run.sh"}, Command: []string{"sh", "/verifier/run.sh"}, Selector: selector, DisplayNoun: "unit", Produces: "report", Outputs: []string{}, Requires: map[string]string{}, Toolchain: pkl.Toolchain{Kind: "strong"}, Deterministic: true, Network: "none", Timeout: 30}
}

type fixture struct {
	root, home  string
	declaration declaration.Declaration
}

func makeFixture(t *testing.T, verifiers []pkl.Verifier, scripts map[string]string, files map[string]string) fixture {
	t.Helper()
	f := fixture{root: t.TempDir(), home: t.TempDir()}
	git(t, f.root, "init")
	for path, text := range files {
		write(t, f.root, path, text)
	}
	repository := pkl.VerifyRepository{Verifiers: map[string]pkl.Verifier{}, Gates: map[string]pkl.Gate{}, Focuses: map[string]pkl.Focus{}}
	for _, v := range verifiers {
		repository.Verifiers[v.ID] = v
		write(t, f.root, v.Program[0], scripts[v.ID])
	}
	data, err := store.JSONBytes(repository)
	if err != nil {
		t.Fatal(err)
	}
	f.declaration, err = declaration.Decode(data)
	if err != nil {
		t.Fatal(err)
	}
	git(t, f.root, "add", ".")
	return f
}
func (f fixture) options(t *testing.T) engine.Options {
	t.Helper()
	snapshot, err := tree.Compute(context.Background(), f.root, f.home)
	if err != nil {
		t.Fatal(err)
	}
	return engine.Options{Home: f.home, Tree: snapshot, Declaration: f.declaration, Concurrency: 2, Random: func() float64 { return 0 }}
}
func observeTree(t *testing.T, options engine.Options) observe.Observation {
	t.Helper()
	result, err := engine.Observe(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete {
		t.Fatalf("coverage incomplete: %+v", result)
	}
	return result
}
func recomputed(o observe.Observation) []string {
	ids := []string{}
	for _, result := range o.Results {
		if result.Provenance.Recomputed {
			ids = append(ids, result.UnitID)
		}
	}
	return ids
}
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []string{}
	}
	if err != nil {
		t.Fatal(err)
	}
	out := []string{}
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}

func TestVerifyOwnCachesExactStdoutAndRerunsOnlyChangedClosure(t *testing.T) {
	const selection = "{\"units\":[{\"unitId\":\"a\",\"inputs\":[\"src/a\"]},{\"unitId\":\"b\",\"inputs\":[\"src/b\"]}]}\n"
	script := `set -eu
if [ "$VERIFY_ROLE" = select ]; then
 test ! -e /scratch
 printf '%s\n' '{"units":[{"unitId":"a","inputs":["src/a"]},{"unitId":"b","inputs":["src/b"]}]}'
 exit
fi
cat /etc/hostname
test "$(find /work -type f | wc -l)" = 1
test ! -e /work/untracked
test ! -e /work/.git
test ! -e /work/src/c
cat "/work/src/$VERIFY_UNIT"
` + emptyReport
	v := verifier("units", pkl.Selector{Kind: "Own"})
	f := makeFixture(t, []pkl.Verifier{v}, map[string]string{"units": script}, map[string]string{"src/a": "a", "src/b": "b", "src/c": "c"})
	write(t, f.root, "untracked", "secret")
	options := f.options(t)
	// Once captured, edits to checkout bytes must not change the Run inputs.
	write(t, f.root, "src/a", "later")
	write(t, f.root, v.Program[0], "exit 72")
	first := observeTree(t, options)
	if !slices.Equal(recomputed(first), []string{"a", "b"}) || len(first.Unknown) != 0 {
		t.Fatalf("first: %+v", first)
	}
	records := names(t, filepath.Join(f.home, "ac"))
	if len(records) != 3 {
		t.Fatalf("expected selection and two run Actions: %v", records)
	}
	foundSelection := false
	for _, name := range records {
		data, err := os.ReadFile(filepath.Join(f.home, "ac", name))
		if err != nil {
			t.Fatal(err)
		}
		var record store.IndexRecord
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record.Attestation.UnitID != "*" {
			stdout, err := store.Read(f.home, record.Stdout)
			if err != nil || !bytes.HasSuffix(stdout, []byte("\n"+record.Attestation.UnitID)) {
				t.Fatalf("run did not consume captured input bytes: %q %v", stdout, err)
			}
		}
		if digest := record.Attestation.Outputs["select.json"]; digest != "" {
			got, err := store.Read(f.home, digest)
			if err != nil || string(got) != selection {
				t.Fatalf("stdout bytes %q %v", got, err)
			}
			foundSelection = true
		}
	}
	if !foundSelection {
		t.Fatal("selection not cached")
	}
	write(t, f.root, v.Program[0], script)
	blobs := names(t, filepath.Join(f.home, "cas"))
	second := observeTree(t, options)
	if !slices.Equal(names(t, filepath.Join(f.home, "cas")), blobs) {
		t.Fatal("cache hit executed again and stored new process evidence")
	}
	if len(recomputed(second)) != 0 || !slices.Equal(names(t, filepath.Join(f.home, "ac")), records) {
		t.Fatalf("cache not reused: %+v", second)
	}
	for _, result := range second.Results {
		if result.Provenance.CarriedFrom != first.Tree {
			t.Fatalf("provenance: %+v", result)
		}
	}
	changed := observeTree(t, f.options(t))
	if !slices.Equal(recomputed(changed), []string{"a"}) {
		t.Fatalf("wrong closure rerun: %+v", changed)
	}
	write(t, f.root, "src/c", "unrelated edit")
	if got := observeTree(t, f.options(t)); len(recomputed(got)) != 0 {
		t.Fatalf("unrelated edit reran: %+v", got)
	}
	if len(names(t, filepath.Join(f.home, "exec"))) != 0 {
		t.Fatal("staging leaked")
	}
}

func TestVerifyToolFailuresRetryAndBlockUpstream(t *testing.T) {
	build := verifier("build", pkl.Selector{Kind: "Paths", Patterns: []string{"src/a"}})
	build.Produces, build.Outputs = "artifact", []string{"tool"}
	consumer := verifier("consumer", pkl.Selector{Kind: "WholeTree"})
	consumer.Requires = map[string]string{"tools": "build"}
	independent := verifier("independent", pkl.Selector{Kind: "WholeTree"})
	scripts := map[string]string{"build": `printf evidence; printf failure >&2; exit 23`, "consumer": emptyReport, "independent": emptyReport}
	f := makeFixture(t, []pkl.Verifier{consumer, independent, build}, scripts, map[string]string{"src/a": "input"})
	for range 2 {
		got := observeTree(t, f.options(t))
		if len(got.Results) != 1 || got.Results[0].Verifier != "independent" || len(got.Unknown) != 2 || got.Unknown[0].Reason != "exit 23: failure" || got.Unknown[1].Reason != "upstream build unknown" {
			t.Fatalf("failure: %+v", got)
		}
		if len(names(t, filepath.Join(f.home, "ac"))) != 1 {
			t.Fatal("tool failure attested")
		}
	}
	registered, err := declaration.Order(f.declaration)
	if err != nil {
		t.Fatal(err)
	}
	if len(names(t, filepath.Join(f.home, "witness", "build", registered[0].Version))) != 1 {
		t.Fatal("failure not witnessed immutably")
	}
}

func TestVerifyUpstreamArtifactsAndProgramComeFromStore(t *testing.T) {
	build := verifier("build", pkl.Selector{Kind: "Paths", Patterns: []string{"src/a"}})
	build.Produces, build.Outputs = "artifact", []string{"tool"}
	consumer := verifier("consumer", pkl.Selector{Kind: "Paths", Patterns: []string{"src/b"}})
	consumer.Requires = map[string]string{"tools": "build"}
	scripts := map[string]string{"build": `set -eu; cp /work/src/a /out/tool; chmod +x /out/tool; ln -s tool /out/uncollected`, "consumer": `set -eu; test ! -e /work/src/a; test -x /upstream/tools/tool; test "$(cat /upstream/tools/tool)" = artifact; test -f /verifier/run.sh; ` + emptyReport}
	f := makeFixture(t, []pkl.Verifier{consumer, build}, scripts, map[string]string{"src/a": "artifact", "src/b": "consumer"})
	first := observeTree(t, f.options(t))
	if len(first.Results) != 2 || len(first.Unknown) != 0 {
		t.Fatalf("artifact: %+v", first)
	}
	write(t, f.root, "src/a", "changed artifact")
	second := observeTree(t, f.options(t))
	if len(second.Results) != 1 || second.Results[0].Verifier != "build" || len(second.Unknown) != 1 {
		t.Fatalf("consumer must run against changed upstream: %+v", second)
	}
}

func TestVerifyReportsRefuseSymlinksMalformedAndUndeclaredFiles(t *testing.T) {
	for name, script := range map[string]string{
		"symlink":    `printf '{"unitId":"check","diagnostics":[]}' > /out/actual; ln -s actual /out/report.json`,
		"malformed":  `printf null > /out/report.json`,
		"undeclared": `printf '{"unitId":"check","diagnostics":[{"errorKey":"e","severity":"error","message":"m","file":"src/b"}]}' > /out/report.json`,
		"missing":    `true`,
	} {
		t.Run(name, func(t *testing.T) {
			v := verifier("check", pkl.Selector{Kind: "Paths", Patterns: []string{"src/a"}})
			f := makeFixture(t, []pkl.Verifier{v}, map[string]string{"check": script}, map[string]string{"src/a": "a", "src/b": "b"})
			got := observeTree(t, f.options(t))
			if len(got.Unknown) != 1 || len(got.Results) != 0 || len(names(t, filepath.Join(f.home, "ac"))) != 0 {
				t.Fatalf("refusal attested: %+v", got)
			}
		})
	}
}

func TestVerifyScratchAuditsMatchOrWitnessBeforePublication(t *testing.T) {
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "matched", true: "mismatch"}[mismatch], func(t *testing.T) {
			script := `set -eu; test ! -e /scratch/discarded; test ! -e /work/untracked; warm=no; if [ -e /scratch/cache ]; then warm=yes; fi; printf retained >/scratch/cache; printf discarded >/scratch/discarded; mkfifo /scratch/ignored-fifo; `
			if mismatch {
				script += `printf '{"unitId":"check","diagnostics":[{"errorKey":"cache","severity":"message","message":"%s"}]}' "$warm" >/out/report.json`
			} else {
				script += emptyReport
			}
			v := verifier("check", pkl.Selector{Kind: "Paths", Patterns: []string{"src/a"}})
			rate := 1.0
			v.Toolchain = pkl.Toolchain{Kind: "weak", Retained: []string{"cache"}, AuditRate: &rate}
			f := makeFixture(t, []pkl.Verifier{v}, map[string]string{"check": script}, map[string]string{"src/a": "first"})
			first := observeTree(t, f.options(t))
			if len(first.Results) != 1 || first.Results[0].Scratch != "absent" {
				t.Fatalf("cold start: %+v", first)
			}
			write(t, f.root, "src/a", "second")
			options := f.options(t)
			samples := 0
			options.Random = func() float64 { samples++; return 0 }
			second := observeTree(t, options)
			if samples != 1 {
				t.Fatalf("eligible audit sampled %d times", samples)
			}
			if mismatch {
				if len(second.Unknown) != 1 || second.Unknown[0].Reason != "scratch audit mismatch" || len(second.Results) != 0 || len(names(t, filepath.Join(f.home, "ac"))) != 1 {
					t.Fatalf("mismatch: %+v", second)
				}
				ordered, err := declaration.Order(f.declaration)
				if err != nil {
					t.Fatal(err)
				}
				if len(names(t, filepath.Join(f.home, "witness", "check", ordered[0].Version))) != 1 || len(names(t, filepath.Join(f.home, "scratch", "check"))) != 0 {
					t.Fatal("audit must witness and drop Scratch")
				}
				retried := observeTree(t, options)
				if len(retried.Results) != 1 || retried.Results[0].Scratch != "absent" || samples != 1 {
					t.Fatalf("witnessed version restored Scratch: %+v", retried)
				}
			} else if len(second.Results) != 1 || second.Results[0].Scratch != "overlaid" || second.Results[0].Audit != "matched" {
				t.Fatalf("matched audit: %+v", second)
			}
		})
	}
}

func TestVerifyCLITracksSessionsAndAllIsReadOnly(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	git(t, root, "init")
	write(t, root, "src/a", "TODO\n")
	write(t, root, "workbench.pkl", `amends "modulepath:/Repository.pkl"
verifiers { ["lint"] = import("lint.pkl") }
gates { ["commit"] = new Gate { requires { "lint" }; blocking { "error" } } }
focuses { ["other"] = new Focus { files { "src/b" } } }
`)
	write(t, root, "lint.pkl", `amends "modulepath:/Verifier.pkl"
id = "lint"
image = "`+image+`"
program { "run.sh" }
command { "sh"; "/verifier/run.sh" }
selector = new Paths { patterns { "src/a" } }
displayNoun = "source file"
produces = "report"
`)
	write(t, root, "run.sh", `printf '{"unitId":"lint","diagnostics":[{"errorKey":"todo","severity":"error","message":"TODO","file":"src/a","range":{"start":{"line":1,"column":1}}}]}' >/out/report.json`)
	git(t, root, "add", ".")
	call := func(args ...string) (int, string) {
		t.Helper()
		var out, diagnostics bytes.Buffer
		code, err := engine.Main(context.Background(), append([]string{"--home", home, "--session", "session"}, args...), engine.IO{WorkingDirectory: func() (string, error) { return root, nil }, Stdout: &out, Stderr: &diagnostics})
		if err != nil || diagnostics.Len() != 0 {
			t.Fatalf("CLI: %v %s", err, &diagnostics)
		}
		return code, out.String()
	}
	code, first := call()
	if code != 1 || !strings.Contains(first, "1 new") {
		t.Fatalf("first: %d %s", code, first)
	}
	seen := filepath.Join(home, "sessions", "session", "last-seen.json")
	before, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	code, second := call()
	if code != 1 || !strings.Contains(second, "1 unchanged") {
		t.Fatalf("second: %d %s", code, second)
	}
	code, all := call("--all")
	after, err := os.ReadFile(seen)
	if err != nil {
		t.Fatal(err)
	}
	if code != 1 || !strings.Contains(all, "todo") || !bytes.Equal(before, after) {
		t.Fatal("--all changed attention state")
	}
	code, _ = call("focus:other")
	if code != 0 {
		t.Fatal("focus did not affect exit")
	}
	code, _ = call("focus:other", "--gate", "commit")
	if code != 1 {
		t.Fatal("focus weakened gate")
	}
	code, reset := call("--reset")
	if code != 1 || !strings.Contains(reset, "1 new") {
		t.Fatalf("reset: %d %s", code, reset)
	}
}

func TestVerifyCLIHelpAndUsageDoNotReadCheckout(t *testing.T) {
	for _, args := range [][]string{{"--help"}, {"--unknown"}} {
		var out, diagnostics bytes.Buffer
		code, err := engine.Main(context.Background(), args, engine.IO{WorkingDirectory: func() (string, error) { t.Fatal("read checkout before parsing"); return "", nil }, Stdout: &out, Stderr: &diagnostics})
		want := 0
		if args[0] == "--unknown" {
			want = 2
		}
		if err != nil || code != want {
			t.Fatalf("parse: %d %v", code, err)
		}
	}
}

func TestVerifyEvaluateUsesFrozenProjectAndLock(t *testing.T) {
	const uri = "package://offline.invalid/engine@1.0.0"
	root, home := t.TempDir(), t.TempDir()
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	file, err := writer.Create("value.pkl")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(`value = "frozen"`)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	metadata, err := store.JSONBytes(map[string]any{"name": "engine", "packageUri": uri, "version": "1.0.0", "packageZipUrl": "https://offline.invalid/engine.zip", "packageZipChecksums": map[string]string{"sha256": store.SHA256(archive.Bytes())}, "dependencies": map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	metadataPath, archivePath, err := declaration.PackageCachePaths(home, uri)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metadataPath, metadata, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, archive.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	lock, err := store.JSONBytes(map[string]any{"schemaVersion": 1, "resolvedDependencies": map[string]any{"package://offline.invalid/engine@1": map[string]any{"type": "remote", "uri": "projectpackage://offline.invalid/engine@1.0.0", "checksums": map[string]string{"sha256": store.SHA256(metadata)}}}})
	if err != nil {
		t.Fatal(err)
	}
	git(t, root, "init")
	write(t, root, "workbench.pkl", `amends "modulepath:/Repository.pkl"
focuses { ["package"] = new Focus { codes { import("@fixture/value.pkl").value } } }
`)
	write(t, root, "PklProject", `amends "pkl:Project"
dependencies { ["fixture"] { uri = "`+uri+`" } }
`)
	write(t, root, "PklProject.deps.json", string(lock))
	git(t, root, "add", ".")
	snapshot, err := tree.Compute(context.Background(), root, home)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"workbench.pkl", "PklProject", "PklProject.deps.json"} {
		write(t, root, path, "broken after capture")
	}
	value, err := engine.Evaluate(context.Background(), declaration.AmbientPkl(), snapshot, home)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(value.Repository.Focuses["package"].Codes, []string{"frozen"}) {
		t.Fatalf("named dependency did not use frozen project and lock: %+v", value.Repository.Focuses)
	}
}

func TestVerifyComposedActionAndReportDigestsMatchReference(t *testing.T) {
	data, err := os.ReadFile("testdata/action.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Manifest                pkl.Verifier
		Files                   map[string]string
		ActionKey, ReportDigest string
	}
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	script := golden.Files[golden.Manifest.Program[0]]
	f := makeFixture(t, []pkl.Verifier{golden.Manifest}, map[string]string{"check": script}, golden.Files)
	got := observeTree(t, f.options(t))
	if len(got.Results) != 1 || len(got.Unknown) != 0 {
		t.Fatalf("reference fixture failed: %+v", got)
	}
	result := got.Results[0]
	if result.ActionKey != golden.ActionKey || result.Attestation.Outputs["report.json"] != golden.ReportDigest {
		t.Fatalf("reference parity: %+v; want action %s, Report %s", result, golden.ActionKey, golden.ReportDigest)
	}
}

func TestVerifyConcurrencyIsBounded(t *testing.T) {
	const bunImage = "oven/bun@sha256:9114c058aeae42162ee16dd5084b95fe9473970bb6bcb5b232ab1630f0546895"
	script := `import {writeFileSync} from "node:fs";
if(process.env.VERIFY_ROLE==="select") {console.log(JSON.stringify({units:Array.from({length:6},(_,i)=>({unitId:String(i),inputs:[]}))}));}
else {const start=Date.now();await Bun.sleep(1200);writeFileSync("/out/report.json",JSON.stringify({unitId:process.env.VERIFY_UNIT,diagnostics:[]}));console.log(JSON.stringify({start,end:Date.now()}));}`
	v := verifier("units", pkl.Selector{Kind: "Own"})
	v.Image = bunImage
	v.Program = []string{"verifiers/units/run.ts"}
	v.Command = []string{"bun", "/verifier/run.ts"}
	f := makeFixture(t, []pkl.Verifier{v}, map[string]string{"units": script}, nil)
	got := observeTree(t, f.options(t))
	if len(got.Results) != 6 || len(got.Unknown) != 0 {
		t.Fatalf("bounded run failed: %+v", got)
	}
	type event struct {
		at     int64
		change int
	}
	events := []event{}
	for _, result := range got.Results {
		record, err := store.Lookup(f.home, result.ActionKey, got.Tree, true)
		if err != nil || record == nil {
			t.Fatalf("index: %v", err)
		}
		stdout, err := store.Read(f.home, record.Stdout)
		if err != nil {
			t.Fatal(err)
		}
		var interval struct{ Start, End int64 }
		if err := json.Unmarshal(stdout, &interval); err != nil {
			t.Fatal(err)
		}
		events = append(events, event{interval.Start, 1}, event{interval.End, -1})
	}
	slices.SortFunc(events, func(a, b event) int {
		if a.at < b.at {
			return -1
		}
		if a.at > b.at {
			return 1
		}
		return a.change - b.change
	})
	active, maxActive := 0, 0
	for _, e := range events {
		active += e.change
		maxActive = max(maxActive, active)
	}
	if maxActive != 2 {
		t.Fatalf("expected two concurrent Units at most and evidence of parallelism, got %d", maxActive)
	}

}

func TestVerifyNondeterministicCacheIncludesTree(t *testing.T) {
	for _, deterministic := range []bool{true, false} {
		t.Run(map[bool]string{true: "deterministic", false: "nondeterministic"}[deterministic], func(t *testing.T) {
			v := verifier("check", pkl.Selector{Kind: "Paths", Patterns: []string{"src/a"}})
			v.Deterministic = deterministic
			f := makeFixture(t, []pkl.Verifier{v}, map[string]string{"check": emptyReport}, map[string]string{"src/a": "selected", "other": "original"})
			first := observeTree(t, f.options(t))
			if len(first.Results) != 1 {
				t.Fatalf("first: %+v", first)
			}
			write(t, f.root, "other", "changed")
			second := observeTree(t, f.options(t))
			if len(second.Results) != 1 || second.Results[0].ActionKey != first.Results[0].ActionKey || second.Results[0].Provenance.Recomputed == deterministic {
				t.Fatalf("Tree salt: %+v", second)
			}
		})
	}
}

func TestVerifyCancellationDuringRunIsUnknownAndCleansStage(t *testing.T) {
	f := makeFixture(t, []pkl.Verifier{verifier("check", pkl.Selector{Kind: "WholeTree"})}, map[string]string{"check": `printf started >/out/started; sleep 60`}, nil)
	options := f.options(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	type observed struct {
		o   observe.Observation
		err error
	}
	done := make(chan observed, 1)
	go func() { o, err := engine.Observe(ctx, options); done <- observed{o, err} }()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	started := false
	for !started {
		select {
		case result := <-done:
			t.Fatalf("execution ended before container started: %+v", result)
		case <-ctx.Done():
			result := <-done
			t.Fatalf("container never started: %+v", result)
		case <-ticker.C:
			files, err := filepath.Glob(filepath.Join(f.home, "exec", "action-*", "out", "started"))
			if err != nil {
				t.Fatal(err)
			}
			started = len(files) > 0
		}
	}
	cancel()
	result := <-done
	if result.err != nil || len(result.o.Results) != 0 || len(result.o.Unknown) != 1 || !strings.Contains(result.o.Unknown[0].Reason, "context canceled") {
		t.Fatalf("cancellation attested or classified as subject failure: %+v", result)
	}
	if len(names(t, filepath.Join(f.home, "exec"))) != 0 || len(names(t, filepath.Join(f.home, "ac"))) != 0 {
		t.Fatal("cancelled execution leaked staging or attested")
	}
}

func TestVerifyConcurrentPublicationReturnsFirstStoredAttestation(t *testing.T) {
	script := `set -eu; printf cache >/scratch/cache; printf '{"unitId":"check","diagnostics":[{"errorKey":"host","severity":"message","message":"%s"}]}' "$(cat /etc/hostname)" >/out/report.json`
	v := verifier("check", pkl.Selector{Kind: "Paths", Patterns: []string{"src/a"}})
	rate := 0.0
	v.Toolchain = pkl.Toolchain{Kind: "weak", Retained: []string{"cache"}, AuditRate: &rate}
	f := makeFixture(t, []pkl.Verifier{v}, map[string]string{"check": script}, map[string]string{"src/a": "initial"})
	initial := observeTree(t, f.options(t))
	if len(initial.Results) != 1 {
		t.Fatalf("initial: %+v", initial)
	}
	write(t, f.root, "src/a", "changed")
	options := f.options(t)
	reached, release := make(chan struct{}, 2), make(chan struct{})
	options.Random = func() float64 { reached <- struct{}{}; <-release; return 0 }
	type observed struct {
		o   observe.Observation
		err error
	}
	done := make(chan observed, 2)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	for range 2 {
		go func() { o, err := engine.Observe(ctx, options); done <- observed{o, err} }()
	}
	for range 2 {
		select {
		case <-reached:
		case <-ctx.Done():
			close(release)
			<-done
			<-done
			t.Fatal("both concurrent Runs did not reach publication")
		}
	}
	close(release)
	first, second := <-done, <-done
	if first.err != nil || second.err != nil || len(first.o.Results) != 1 || len(second.o.Results) != 1 {
		t.Fatalf("publication: %+v %+v", first, second)
	}
	a, b := first.o.Results[0], second.o.Results[0]
	if a.Attestation.Outputs["report.json"] != b.Attestation.Outputs["report.json"] || a.Provenance.Recomputed == b.Provenance.Recomputed {
		t.Fatalf("loser did not receive permanent Attestation: %+v %+v", a, b)
	}
	ordered, err := declaration.Order(f.declaration)
	if err != nil {
		t.Fatal(err)
	}
	if len(names(t, filepath.Join(f.home, "witness", "check", ordered[0].Version))) != 1 || len(names(t, filepath.Join(f.home, "ac"))) != 2 {
		t.Fatal("differing duplicate Run not witnessed or first replaced")
	}
}
