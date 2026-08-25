package acceptance_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestImmutableV020PackageRetainsHistoricalRootLayout(t *testing.T) {
	base := "https://github.com/phosphorco/workbench-go/releases/download/0.2.0/"
	metadata := downloadPublicReleaseAsset(t, base+"workbench@0.2.0")
	metadataChecksum := downloadPublicReleaseAsset(t, base+"workbench@0.2.0.sha256")
	archive := downloadPublicReleaseAsset(t, base+"workbench@0.2.0.zip")
	archiveChecksum := downloadPublicReleaseAsset(t, base+"workbench@0.2.0.zip.sha256")
	assertPublicReleaseChecksum(t, "workbench@0.2.0", metadata, metadataChecksum)
	assertPublicReleaseChecksum(t, "workbench@0.2.0.zip", archive, archiveChecksum)
	var published releaseMetadata
	if err := json.Unmarshal(metadata, &published); err != nil {
		t.Fatalf("decode immutable 0.2 metadata: %v", err)
	}
	if published.Version != "0.2.0" || published.PackageURI != v020ReleasePackageURI || published.PackageZIPURL != v020ReleasePackageZIP {
		t.Fatalf("immutable 0.2 metadata = %#v", published)
	}

	archivePath := filepath.Join(t.TempDir(), "workbench@0.2.0.zip")
	if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
		t.Fatal(err)
	}
	consumer := filepath.Join(t.TempDir(), "consumer.pkl")
	if err := os.WriteFile(consumer, []byte(`amends "modulepath:/PackageScopeRepository.pkl"

scope = "@historical"
packages {
  ["@historical/app"] {}
}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("pkl", "eval", "--module-path", filepath.ToSlash(archivePath), "--format", "json", consumer)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("immutable 0.2 PackageScope contract rejected its historical declaration: %v\n%s", err, output)
	}
	if v020ReleasePackageURI == currentReleasePackageURI || v020ReleasePackageZIP == currentReleasePackageZIP {
		t.Fatal("0.2 compatibility designation collapsed into the current candidate")
	}
}
