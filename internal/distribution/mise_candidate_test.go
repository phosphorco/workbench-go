package distribution

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
	goruntime "runtime"
	"strings"
	"testing"
)

func TestMiseGitHubBackendAutodetectsCandidateArchive(t *testing.T) {
	if goruntime.GOOS != "linux" && goruntime.GOOS != "darwin" {
		t.Skip("Workbench 0.4.0 does not distribute this operating system")
	}
	mise, err := exec.LookPath("mise")
	if err != nil {
		t.Skip("Mise is not available for the distribution candidate oracle")
	}
	root := t.TempDir()
	inputs := archiveInputs(t)
	platform := Platform{OS: goruntime.GOOS, Arch: goruntime.GOARCH}
	assetName, err := AssetName("0.4.0", platform)
	if err != nil {
		t.Fatalf("AssetName(): %v", err)
	}
	archive := filepath.Join(root, assetName)
	if err := WriteArchive(archive, inputs); err != nil {
		t.Fatalf("WriteArchive(): %v", err)
	}
	archiveBytes, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(archiveBytes)
	checksumBytes := []byte(hex.EncodeToString(digest[:]) + "\n")

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		baseURL := "http://" + request.Host
		assets := []map[string]any{
			{"name": "workbench@0.4.0.zip", "browser_download_url": baseURL + "/assets/contracts", "url": baseURL + "/api/assets/contracts"},
		}
		for _, name := range []string{
			"workbench-0.4.0-linux-arm64.tar.gz",
			"workbench-0.4.0-linux-x64.tar.gz",
			"workbench-0.4.0-macos-arm64.tar.gz",
			"workbench-0.4.0-macos-x64.tar.gz",
		} {
			assets = append(assets,
				map[string]any{"name": name, "browser_download_url": baseURL + "/assets/" + name, "url": baseURL + "/api/assets/" + name},
				map[string]any{"name": name + ".sha256", "browser_download_url": baseURL + "/assets/" + name + ".sha256", "url": baseURL + "/api/assets/" + name + ".sha256"},
			)
		}
		release := map[string]any{
			"tag_name":   "0.4.0",
			"created_at": "2026-08-24T00:00:00Z",
			"draft":      false, "prerelease": false,
			"assets": assets,
		}
		switch request.URL.Path {
		case "/repos/phosphorco/workbench-go/releases":
			response.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(response).Encode([]any{release}); err != nil {
				t.Errorf("encode fake GitHub releases: %v", err)
			}
		case "/repos/phosphorco/workbench-go/releases/tags/0.4.0":
			response.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(response).Encode(release); err != nil {
				t.Errorf("encode fake GitHub release: %v", err)
			}
		case "/assets/" + assetName, "/api/assets/" + assetName:
			_, _ = response.Write(archiveBytes)
		case "/assets/" + assetName + ".sha256", "/api/assets/" + assetName + ".sha256":
			_, _ = response.Write(checksumBytes)
		default:
			http.NotFound(response, request)
		}
	}))
	t.Cleanup(server.Close)

	data := filepath.Join(root, "mise-data")
	tool := fmt.Sprintf("github:phosphorco/workbench-go[api_url=%s,github_attestations=false]@0.4.0", server.URL)
	command := exec.Command(mise, "install", tool)
	command.Dir = root
	command.Env = append(os.Environ(),
		"HOME="+filepath.Join(root, "home"),
		"MISE_DATA_DIR="+data,
		"MISE_CACHE_DIR="+filepath.Join(root, "mise-cache"),
		"MISE_STATE_DIR="+filepath.Join(root, "mise-state"),
		"MISE_CONFIG_DIR="+filepath.Join(root, "mise-config"),
		"MISE_GITHUB_GH_CLI_TOKENS=false",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Mise candidate install: %v\n%s", err, output)
	}
	installRoot := filepath.Join(data, "installs", "github-phosphorco-workbench-go", "0.4.0")
	if _, err := os.Stat(filepath.Join(installRoot, "bin", "workbench")); err != nil {
		t.Fatalf("Mise did not expose bin/workbench from %q: %v", assetName, err)
	}
	if _, err := os.Stat(filepath.Join(installRoot, "libexec", "workbench", "pkl")); err != nil {
		t.Fatalf("Mise did not retain the private Pkl runtime: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(installRoot, "bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "workbench" {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("Mise-exposed binaries = %s, want only workbench", strings.Join(names, ", "))
	}
}
