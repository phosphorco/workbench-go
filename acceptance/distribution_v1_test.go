package acceptance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/phosphorco/workbench-go/internal/distribution"
)

// The fake release API proves Mise's native asset autodetection and archive
// layout without publication. The checked workflow marker below keeps the real
// anonymous GitHub release + clean runner setup boundary explicit for final proof.
func TestDistributionV1ArchiveAndMiseCandidateNamesPublicBoundary(t *testing.T) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		t.Skip("Workbench V1 distribution does not target this development host")
	}
	mise, err := exec.LookPath("mise")
	if err != nil {
		t.Skip("Mise is unavailable; final public acceptance remains mandatory")
	}
	root := t.TempDir()
	inputs := distributionV1Inputs(t, root)
	platform := distribution.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	assetName, err := distribution.AssetName("0.3.0", platform)
	if err != nil {
		t.Fatal(err)
	}
	first := filepath.Join(root, "first", assetName)
	second := filepath.Join(root, "second", assetName)
	if err := distribution.WriteArchive(first, inputs); err != nil {
		t.Fatal(err)
	}
	if err := distribution.WriteArchive(second, inputs); err != nil {
		t.Fatal(err)
	}
	firstBytes, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	secondBytes, err := os.ReadFile(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstBytes) != string(secondBytes) {
		t.Fatal("identical release inputs did not produce a deterministic archive")
	}
	digest := sha256.Sum256(firstBytes)
	checksum := []byte(hex.EncodeToString(digest[:]) + "\n")

	server := httptest.NewUnstartedServer(nil)
	server.Config.Handler = http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assets := []map[string]any{}
		for _, name := range []string{
			"workbench-0.3.0-linux-arm64.tar.gz", "workbench-0.3.0-linux-x64.tar.gz",
			"workbench-0.3.0-macos-arm64.tar.gz", "workbench-0.3.0-macos-x64.tar.gz",
		} {
			assets = append(assets,
				map[string]any{"name": name, "browser_download_url": server.URL + "/assets/" + name, "url": server.URL + "/api/assets/" + name},
				map[string]any{"name": name + ".sha256", "browser_download_url": server.URL + "/assets/" + name + ".sha256", "url": server.URL + "/api/assets/" + name + ".sha256"},
			)
		}
		release := map[string]any{"tag_name": "0.3.0", "created_at": "2026-08-24T00:00:00Z", "draft": false, "prerelease": false, "assets": assets}
		switch request.URL.Path {
		case "/repos/phosphorco/workbench-go/releases":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode([]any{release})
		case "/repos/phosphorco/workbench-go/releases/tags/0.3.0":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(release)
		case "/assets/" + assetName, "/api/assets/" + assetName:
			_, _ = response.Write(firstBytes)
		case "/assets/" + assetName + ".sha256", "/api/assets/" + assetName + ".sha256":
			_, _ = response.Write(checksum)
		default:
			http.NotFound(response, request)
		}
	})
	server.Start()
	t.Cleanup(server.Close)

	data := filepath.Join(root, "mise-data")
	identity := fmt.Sprintf("github:phosphorco/workbench-go[api_url=%s,github_attestations=false]@0.3.0", server.URL)
	command := exec.Command(mise, "install", identity)
	command.Dir = root
	command.Env = append(os.Environ(),
		"HOME="+filepath.Join(root, "home"), "MISE_DATA_DIR="+data,
		"MISE_CACHE_DIR="+filepath.Join(root, "mise-cache"), "MISE_STATE_DIR="+filepath.Join(root, "mise-state"),
		"MISE_CONFIG_DIR="+filepath.Join(root, "mise-config"), "MISE_GITHUB_GH_CLI_TOKENS=false", "MISE_GITHUB_USE_GIT_CREDENTIALS=false",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Mise local-candidate install: %v\n%s", err, output)
	}
	installed := filepath.Join(data, "installs", "github-phosphorco-workbench-go", "0.3.0")
	for _, path := range []string{"bin/workbench", "libexec/workbench/pkl", "libexec/workbench/bun"} {
		if _, err := os.Stat(filepath.Join(installed, filepath.FromSlash(path))); err != nil {
			t.Fatalf("Mise installation lacks %s: %v", path, err)
		}
	}

	workflow := readFile(t, filepath.Join("..", ".github", "workflows", "release-acceptance.yml"))
	for _, marker := range []string{"mise install github:phosphorco/workbench-go@0.3.0", "workbench setup", "GH_TOKEN: \"\"", "GITHUB_TOKEN: \"\""} {
		if !strings.Contains(workflow, marker) {
			t.Errorf("final public acceptance workflow lacks marker %q", marker)
		}
	}
}

func distributionV1Inputs(t *testing.T, root string) distribution.ArchiveInputs {
	t.Helper()
	file := func(name, contents string, executable bool) string {
		path := filepath.Join(root, "inputs", name)
		writeFile(t, path, contents)
		if executable {
			if err := os.Chmod(path, 0o700); err != nil {
				t.Fatal(err)
			}
		}
		return path
	}
	return distribution.ArchiveInputs{
		Version: "0.3.0", Revision: snapshotPruneV1Commit,
		WorkbenchBinary: file("workbench", "#!/bin/sh\necho workbench\n", true),
		PklBinary:       file("pkl", "#!/bin/sh\necho pkl\n", true), BunBinary: file("bun", "#!/bin/sh\necho bun\n", true),
		RuntimeLock: file("runtime-lock.json", "{}\n", false), WorkbenchLicense: file("workbench-license", "license\n", false),
		PklLicense: file("pkl-license", "license\n", false), PklNotice: file("pkl-notice", "notice\n", false),
		PklThirdPartyNotice: file("pkl-third-party", "third party\n", false), BunLicense: file("bun-license", "license\n", false),
		GoLicense: file("go-license", "license\n", false), GoPatents: file("go-patents", "patents\n", false),
		PklGoLicense: file("pkl-go-license", "license\n", false), PklGoNotice: file("pkl-go-notice", "notice\n", false),
		MsgpackLicense: file("msgpack-license", "license\n", false), TagparserLicense: file("tagparser-license", "license\n", false),
	}
}
