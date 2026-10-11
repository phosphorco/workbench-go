package distribution_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/buildable"
	"github.com/phosphorco/workbench-go/internal/verify/declaration"
	"github.com/phosphorco/workbench-go/internal/verify/distribution"
	"github.com/phosphorco/workbench-go/internal/verify/engine"
	"github.com/phosphorco/workbench-go/internal/verify/observe"
	"github.com/phosphorco/workbench-go/internal/verify/store"
	"github.com/phosphorco/workbench-go/pkl"
)

const payload = "attested executable bytes\n"

func manifestBytes(t *testing.T) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{
		"schemaVersion": 1, "kind": "test-tool", "contractId": "test-v1",
		"source": map[string]string{"revision": "test-revision"}, "capabilities": []string{"run"},
		"producerInputs": map[string]string{"algorithm": "sha256", "digest": store.SHA256([]byte("inputs"))},
		"outputs":        []map[string]any{{"platform": "linux/amd64", "path": "bin/tool", "kind": "executable", "sha256": store.SHA256([]byte(payload)), "size": len(payload), "executable": true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func fixture(t *testing.T) distribution.Options {
	t.Helper()
	home, root := t.TempDir(), t.TempDir()
	v := pkl.Verifier{ID: "tool", Image: "example@sha256:" + strings.Repeat("a", 64), Selector: pkl.Selector{Kind: "WholeTree"}, Produces: "artifact", Outputs: []string{"manifest.json", "bin"}, Toolchain: pkl.Toolchain{Kind: "strong"}, Deterministic: true, Distribution: &pkl.Distribution{Manifest: buildable.ManifestContract{SchemaVersion: 1, Kind: "test-tool", ContractID: "test-v1", ExpectedSource: map[string]string{"revision": "test-revision"}, RequiredSourceFields: []string{"revision"}, RequiredCapabilities: []string{"run"}}, Platform: "linux/amd64", Target: ".ci-build/tool"}}
	raw, err := store.JSONBytes(v)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := declaration.Register(raw)
	if err != nil {
		t.Fatal(err)
	}
	options := distribution.Options{Home: home, Root: root, Tree: "current-tree", Verifier: registered}
	publish(t, &options, manifestBytes(t), payload)
	return options
}

func publish(t *testing.T, options *distribution.Options, manifest []byte, content string) {
	t.Helper()
	blob, err := store.Put(options.Home, []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := store.Put(options.Home, manifest)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := store.PutDirectory(options.Home, []store.DirectoryEntry{{Path: "manifest.json", Digest: metadata, Mode: store.Regular}, {Path: "bin/tool", Digest: blob, Mode: store.Executable}})
	if err != nil {
		t.Fatal(err)
	}
	action := store.SHA256([]byte(digest))
	a := store.Attestation{ActionKey: action, Verifier: store.Verifier{ID: options.Verifier.Manifest.ID, Version: options.Verifier.Version}, UnitID: "tool", Outputs: map[string]string{"artifact": digest}}
	_, err = store.Publish(options.Home, options.Tree, options.Verifier.Manifest.Deterministic, store.IndexRecord{Attestation: a, ComputedIn: options.Tree})
	if err != nil {
		t.Fatal(err)
	}
	options.Observation = observe.Observation{Tree: options.Tree, Complete: true, Results: []observe.Result{{Verifier: a.Verifier.ID, UnitID: a.UnitID, ActionKey: action, Attestation: observe.Attestation{ActionKey: action, Verifier: observe.VerifierVersion{ID: a.Verifier.ID, Version: a.Verifier.Version}, UnitID: a.UnitID, Outputs: a.Outputs}}}}
}

func assertRefused(t *testing.T, options distribution.Options, reason string) {
	t.Helper()
	target := filepath.Join(options.Root, options.Verifier.Manifest.Distribution.Target)
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(target, "previous")
	if err := os.WriteFile(sentinel, []byte("keep"), 0644); err != nil {
		t.Fatal(err)
	}
	_, err := distribution.Promote(options)
	if err == nil || !strings.Contains(err.Error(), reason) {
		t.Fatalf("want refusal %q, got %v", reason, err)
	}
	data, err := os.ReadFile(sentinel)
	if err != nil || string(data) != "keep" {
		t.Fatalf("refusal changed target: %q %v", data, err)
	}
}

func TestPromoteWritesAttestedDigest(t *testing.T) {
	options := fixture(t)
	target := filepath.Join(options.Root, options.Verifier.Manifest.Distribution.Target)
	if err := os.MkdirAll(target, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "obsolete"), []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	receipt, err := distribution.Promote(options)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ArtifactDigest != options.Observation.Results[0].Attestation.Outputs["artifact"] {
		t.Fatalf("wrong digest: %+v", receipt)
	}
	data, err := os.ReadFile(filepath.Join(target, "bin/tool"))
	if err != nil || string(data) != payload {
		t.Fatalf("promoted bytes: %q %v", data, err)
	}
	info, err := os.Stat(filepath.Join(target, "bin/tool"))
	if err != nil || info.Mode().Perm()&0111 == 0 {
		t.Fatalf("executable mode absent: %v", err)
	}
	entries, err := store.Collect(options.Home, target, nil)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := store.PutDirectory(options.Home, entries)
	if err != nil || digest != receipt.ArtifactDigest {
		t.Fatalf("installed digest %s != attested %s: %v", digest, receipt.ArtifactDigest, err)
	}
	if err := os.WriteFile(filepath.Join(target, "bin/tool"), []byte("consumer edit"), 0755); err != nil {
		t.Fatal(err)
	}
	blob, err := store.Read(options.Home, store.SHA256([]byte(payload)))
	if err != nil || string(blob) != payload {
		t.Fatal("promotion shared mutable bytes with store")
	}
}

func TestMacosRefusedWithReason(t *testing.T) {
	options := fixture(t)
	options.Verifier.Manifest.Distribution.Platform = "darwin/arm64"
	assertRefused(t, options, distribution.MacosReason)
}

func TestUnknownUnitRefusedWithReason(t *testing.T) {
	options := fixture(t)
	options.Observation.Unknown = []observe.Unknown{{Verifier: "tool", UnitID: "tool", Reason: "tool exited 19"}}
	assertRefused(t, options, "unit \"tool\" unknown: tool exited 19")
}

func TestFailingUnitRefusedWithReason(t *testing.T) {
	options := fixture(t)
	options.Observation.Results[0].Attestation.Exit = 19
	assertRefused(t, options, "unit \"tool\" failing: exit 19")
}

func TestManifestContractMismatchRefused(t *testing.T) {
	for _, field := range []string{"identity", "source", "required source", "capability", "platform", "hash", "size", "mode", "coverage"} {
		t.Run(field, func(t *testing.T) {
			options := fixture(t)
			var m map[string]any
			if err := json.Unmarshal(manifestBytes(t), &m); err != nil {
				t.Fatal(err)
			}
			output := m["outputs"].([]any)[0].(map[string]any)
			switch field {
			case "identity":
				m["contractId"] = "other"
			case "source":
				m["source"].(map[string]any)["revision"] = "other"
			case "required source":
				options.Verifier.Manifest.Distribution.Manifest.RequiredSourceFields = []string{"missing"}
			case "capability":
				m["capabilities"] = []string{}
			case "platform":
				output["platform"] = "linux/arm64"
			case "hash":
				output["sha256"] = strings.Repeat("b", 64)
			case "size":
				output["size"] = 1
			case "mode":
				output["executable"] = false
			case "coverage":
				m["outputs"] = []any{}
			}
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			publish(t, &options, data, payload)
			assertRefused(t, options, "manifest contract mismatch")
		})
	}
}

func TestPromotionRefusesOtherTreeOrAbsentAttestation(t *testing.T) {
	t.Run("tree", func(t *testing.T) {
		options := fixture(t)
		options.Tree = "other-tree"
		assertRefused(t, options, "current Tree")
	})
	t.Run("index", func(t *testing.T) {
		options := fixture(t)
		if err := os.RemoveAll(filepath.Join(options.Home, "ac")); err != nil {
			t.Fatal(err)
		}
		assertRefused(t, options, "absent from the store")
	})
	t.Run("corrupt bytes", func(t *testing.T) {
		options := fixture(t)
		if err := os.WriteFile(store.BlobPath(options.Home, store.SHA256([]byte(payload))), []byte("corrupt"), 0644); err != nil {
			t.Fatal(err)
		}
		assertRefused(t, options, "digest mismatch")
	})
	t.Run("symlink target", func(t *testing.T) {
		options := fixture(t)
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(options.Root, "escape")); err != nil {
			t.Fatal(err)
		}
		options.Verifier.Manifest.Distribution.Target = "escape/tool"
		if _, err := distribution.Promote(options); err == nil {
			t.Fatal("accepted target outside repository")
		}
		if _, err := os.Stat(filepath.Join(outside, "tool")); !os.IsNotExist(err) {
			t.Fatalf("wrote outside: %v", err)
		}
	})
}

const module = `amends "modulepath:/Verifier.pkl"
id = "tool"
image = "alpine@sha256:5291449c3df73caf6ed85e649dec1b9e818b39a5d8c871e97afc13e9cd5e8fa8"
command { "sh" "/verifier/run.sh" }
program { "run.sh" }
selector = new WholeTree {}
produces = "artifact"
outputs { "bin"; "manifest.json" }
distribution = new Distribution {
 manifest = new ManifestContract {
  schemaVersion = 1
  kind = "test-tool"
  contractId = "test-v1"
  expectedSource { ["revision"] = "test-revision" }
  requiredSourceFields { "revision" }
  requiredCapabilities { "run" }
 }
 platform = "linux/amd64"
 target = ".ci-build/tool"
}
`

func TestPklDistributionArtifactConstraint(t *testing.T) {
	schema, err := filepath.Abs("../../../pkl")
	if err != nil {
		t.Fatal(err)
	}
	for _, produces := range []string{"artifact", "report"} {
		t.Run(produces, func(t *testing.T) {
			source := module
			if produces == "report" {
				source = strings.Replace(source, `produces = "artifact"`, `produces = "report"`, 1)
				source = strings.Replace(source, `outputs { "bin"; "manifest.json" }`, `outputs {}`, 1)
			}
			file := filepath.Join(t.TempDir(), "VERIFIER.pkl")
			if err := os.WriteFile(file, []byte(source), 0644); err != nil {
				t.Fatal(err)
			}
			data, err := exec.Command("pkl", "eval", "-f", "json", "--module-path", schema, file).CombinedOutput()
			if produces == "report" {
				if err == nil || !bytes.Contains(data, []byte("distribution")) {
					t.Fatalf("report distribution accepted: %v %s", err, data)
				}
				return
			}
			if err != nil {
				t.Fatalf("artifact rejected: %v %s", err, data)
			}
			v, err := pkl.DecodeVerifier(data)
			if err != nil {
				t.Fatal(err)
			}
			if v.Distribution == nil || v.Distribution.Manifest.ContractID != "test-v1" {
				t.Fatalf("distribution not decoded: %+v", v.Distribution)
			}
		})
	}
}

func git(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if data, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git: %v %s", err, data)
	}
}

func TestPromoteCLIUsesCurrentTreeAndEngineCache(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	git(t, root, "init")
	sources := map[string]string{"workbench.pkl": `amends "modulepath:/Repository.pkl"
verifiers { ["tool"] = import("VERIFIER.pkl") }
`, "VERIFIER.pkl": module, "run.sh": "set -eu\nmkdir -p /out/bin\ncp /work/payload /out/bin/tool\nchmod +x /out/bin/tool\ncp /work/manifest.json /out/manifest.json\n", "payload": payload, "manifest.json": string(manifestBytes(t))}
	for name, text := range sources {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "add", ".")
	invoke := func() (int, string, string) {
		var out, stderr bytes.Buffer
		code, err := engine.Main(context.Background(), []string{"promote", "tool", "--home", home}, engine.IO{WorkingDirectory: func() (string, error) { return root, nil }, Env: map[string]string{}, Stdout: &out, Stderr: &stderr})
		if err != nil {
			t.Fatal(err)
		}
		return code, out.String(), stderr.String()
	}
	code, out, stderr := invoke()
	if code != 0 || !strings.Contains(out, "artifact ") {
		t.Fatalf("promotion: code %d out %s stderr %s", code, out, stderr)
	}
	digest := strings.TrimSpace(strings.Split(out, "artifact ")[1])
	entries, err := store.Collect(home, filepath.Join(root, ".ci-build/tool"), nil)
	if err != nil {
		t.Fatal(err)
	}
	installed, err := store.PutDirectory(home, entries)
	if err != nil || installed != digest {
		t.Fatalf("CLI digest %s installed %s: %v", digest, installed, err)
	}
	before, err := os.ReadDir(filepath.Join(home, "ac"))
	if err != nil {
		t.Fatal(err)
	}
	code, out, stderr = invoke()
	if code != 0 || !strings.Contains(out, digest) {
		t.Fatalf("cached promotion: %d %s %s", code, out, stderr)
	}
	after, err := os.ReadDir(filepath.Join(home, "ac"))
	if err != nil || len(after) != len(before) {
		t.Fatal("cached promotion changed action index")
	}
	if err := os.WriteFile(filepath.Join(root, "payload"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	code, out, stderr = invoke()
	if code == 0 || !strings.Contains(stderr, "manifest contract mismatch") {
		t.Fatalf("stale artifact promoted: %d %s %s", code, out, stderr)
	}
	data, err := os.ReadFile(filepath.Join(root, ".ci-build/tool/bin/tool"))
	if err != nil || string(data) != payload {
		t.Fatal("failed current-tree promotion changed prior target")
	}
}

func TestPromoteCLIMacosRefusesBeforeExecution(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	git(t, root, "init")
	for name, text := range map[string]string{"workbench.pkl": `amends "modulepath:/Repository.pkl"
verifiers { ["tool"] = import("VERIFIER.pkl") }
`, "VERIFIER.pkl": strings.Replace(module, "linux/amd64", "darwin/arm64", 1), "run.sh": "exit 19"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(text), 0644); err != nil {
			t.Fatal(err)
		}
	}
	git(t, root, "add", ".")
	var out, stderr bytes.Buffer
	code, err := engine.Main(context.Background(), []string{"promote", "tool", "--home", home}, engine.IO{WorkingDirectory: func() (string, error) { return root, nil }, Env: map[string]string{}, Stdout: &out, Stderr: &stderr})
	if err != nil || code != 2 || strings.TrimSpace(stderr.String()) != distribution.MacosReason {
		t.Fatalf("macOS refusal: %d %v %s", code, err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(home, "exec")); !os.IsNotExist(err) {
		t.Fatalf("macOS executed: %v", err)
	}
}
