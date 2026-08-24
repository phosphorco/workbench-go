package distribution

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestRuntimeLockPinsClosedPlatformAndLicenseInventory(t *testing.T) {
	lock, err := LoadRuntimeLock(filepath.Join("..", "..", "release", "runtime-lock.json"))
	if err != nil {
		t.Fatalf("LoadRuntimeLock(): %v", err)
	}
	if lock.WorkbenchVersion != "0.3.0" {
		t.Fatalf("WorkbenchVersion = %q, want 0.3.0", lock.WorkbenchVersion)
	}
	wantDependencies := map[string]string{"go": "1.26.6", "msgpack": "5.4.1", "pkl-go": "0.14.0", "tagparser": "2.0.0"}
	if len(lock.BuildDependencies) != len(wantDependencies) {
		t.Fatalf("build dependencies = %#v, want closed inventory %#v", lock.BuildDependencies, wantDependencies)
	}
	for name, version := range wantDependencies {
		if got := lock.BuildDependencies[name].Version; got != version {
			t.Errorf("build dependency %s version = %q, want %q", name, got, version)
		}
	}
}

func TestAssetNameUsesMiseNativePlatformTokens(t *testing.T) {
	tests := map[string]Platform{
		"workbench-0.3.0-macos-arm64.tar.gz": {OS: "darwin", Arch: "arm64"},
		"workbench-0.3.0-macos-x64.tar.gz":   {OS: "darwin", Arch: "amd64"},
		"workbench-0.3.0-linux-arm64.tar.gz": {OS: "linux", Arch: "arm64"},
		"workbench-0.3.0-linux-x64.tar.gz":   {OS: "linux", Arch: "amd64"},
	}
	for want, platform := range tests {
		got, err := AssetName("0.3.0", platform)
		if err != nil {
			t.Fatalf("AssetName(%#v): %v", platform, err)
		}
		if got != want {
			t.Errorf("AssetName(%#v) = %q, want %q", platform, got, want)
		}
	}
}

func TestWriteArchiveIsByteDeterministicAndHasClosedLayout(t *testing.T) {
	inputs := archiveInputs(t)
	first := filepath.Join(t.TempDir(), "first.tar.gz")
	second := filepath.Join(t.TempDir(), "second.tar.gz")
	if err := WriteArchive(first, inputs); err != nil {
		t.Fatalf("first WriteArchive(): %v", err)
	}
	if err := WriteArchive(second, inputs); err != nil {
		t.Fatalf("second WriteArchive(): %v", err)
	}
	firstBytes, _ := os.ReadFile(first)
	secondBytes, _ := os.ReadFile(second)
	if !reflect.DeepEqual(firstBytes, secondBytes) {
		t.Fatal("identical archive inputs produced different bytes")
	}

	want := []string{
		"workbench-0.3.0/",
		"workbench-0.3.0/bin/",
		"workbench-0.3.0/bin/workbench",
		"workbench-0.3.0/libexec/",
		"workbench-0.3.0/libexec/workbench/",
		"workbench-0.3.0/libexec/workbench/bun",
		"workbench-0.3.0/libexec/workbench/pkl",
		"workbench-0.3.0/share/",
		"workbench-0.3.0/share/licenses/",
		"workbench-0.3.0/share/licenses/bun/",
		"workbench-0.3.0/share/licenses/bun/LICENSE.md",
		"workbench-0.3.0/share/licenses/go/",
		"workbench-0.3.0/share/licenses/go/LICENSE",
		"workbench-0.3.0/share/licenses/go/PATENTS",
		"workbench-0.3.0/share/licenses/msgpack/",
		"workbench-0.3.0/share/licenses/msgpack/LICENSE",
		"workbench-0.3.0/share/licenses/pkl-go/",
		"workbench-0.3.0/share/licenses/pkl-go/LICENSE.txt",
		"workbench-0.3.0/share/licenses/pkl-go/NOTICE.txt",
		"workbench-0.3.0/share/licenses/pkl/",
		"workbench-0.3.0/share/licenses/pkl/LICENSE.txt",
		"workbench-0.3.0/share/licenses/pkl/NOTICE.txt",
		"workbench-0.3.0/share/licenses/pkl/THIRD-PARTY-NOTICES.txt",
		"workbench-0.3.0/share/licenses/tagparser/",
		"workbench-0.3.0/share/licenses/tagparser/LICENSE",
		"workbench-0.3.0/share/licenses/workbench/",
		"workbench-0.3.0/share/licenses/workbench/LICENSE",
		"workbench-0.3.0/share/workbench/",
		"workbench-0.3.0/share/workbench/build.json",
		"workbench-0.3.0/share/workbench/runtime-lock.json",
	}
	if got := archivePaths(t, first); !reflect.DeepEqual(got, want) {
		t.Fatalf("archive paths = %#v, want %#v", got, want)
	}
}

func TestWriteArchiveRefusesMissingWorkbenchLicense(t *testing.T) {
	inputs := archiveInputs(t)
	inputs.WorkbenchLicense = ""
	if err := WriteArchive(filepath.Join(t.TempDir(), "candidate.tar.gz"), inputs); err == nil {
		t.Fatal("WriteArchive succeeded without Workbench license")
	}
}

func archiveInputs(t *testing.T) ArchiveInputs {
	t.Helper()
	root := t.TempDir()
	write := func(name, contents string, mode os.FileMode) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(contents), mode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	return ArchiveInputs{
		Version:             "0.3.0",
		Revision:            "0123456789abcdef0123456789abcdef01234567",
		WorkbenchBinary:     write("workbench", "workbench", 0o755),
		PklBinary:           write("pkl", "pkl", 0o755),
		BunBinary:           write("bun", "bun", 0o755),
		RuntimeLock:         write("runtime-lock.json", "{}\n", 0o644),
		WorkbenchLicense:    write("workbench-license", "license\n", 0o644),
		PklLicense:          write("pkl-license", "license\n", 0o644),
		PklNotice:           write("pkl-notice", "notice\n", 0o644),
		PklThirdPartyNotice: write("pkl-third-party", "third party\n", 0o644),
		BunLicense:          write("bun-license", "license\n", 0o644),
		GoLicense:           write("go-license", "license\n", 0o644),
		GoPatents:           write("go-patents", "patents\n", 0o644),
		PklGoLicense:        write("pkl-go-license", "license\n", 0o644),
		PklGoNotice:         write("pkl-go-notice", "notice\n", 0o644),
		MsgpackLicense:      write("msgpack-license", "license\n", 0o644),
		TagparserLicense:    write("tagparser-license", "license\n", 0o644),
	}
}

func archivePaths(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	var paths []string
	for {
		header, err := reader.Next()
		if err == io.EOF {
			return paths
		}
		if err != nil {
			t.Fatal(err)
		}
		paths = append(paths, header.Name)
	}
}
