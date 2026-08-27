package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/phosphorco/workbench-go/internal/distribution"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "package Workbench release: %v\n", err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	flags := flag.NewFlagSet("workbench-release", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var inputs distribution.ArchiveInputs
	var outputDirectory, goos, goarch string
	flags.StringVar(&inputs.Version, "version", "", "Workbench release version")
	flags.StringVar(&inputs.Revision, "revision", "", "exact Workbench source revision")
	flags.StringVar(&goos, "goos", "", "target Go operating system")
	flags.StringVar(&goarch, "goarch", "", "target Go architecture")
	flags.StringVar(&outputDirectory, "output", "", "release candidate output directory")
	flags.StringVar(&inputs.WorkbenchBinary, "workbench", "", "Workbench executable")
	flags.StringVar(&inputs.PklBinary, "pkl", "", "pinned Pkl executable")
	flags.StringVar(&inputs.BunBinary, "bun", "", "pinned Bun executable")
	flags.StringVar(&inputs.RuntimeLock, "runtime-lock", "", "runtime lock inventory")
	flags.StringVar(&inputs.WorkbenchLicense, "workbench-license", "", "Workbench license")
	flags.StringVar(&inputs.PklLicense, "pkl-license", "", "Pkl license")
	flags.StringVar(&inputs.PklNotice, "pkl-notice", "", "Pkl notice")
	flags.StringVar(&inputs.PklThirdPartyNotice, "pkl-third-party-notice", "", "Pkl third-party notices")
	flags.StringVar(&inputs.BunLicense, "bun-license", "", "Bun license")
	flags.StringVar(&inputs.GoLicense, "go-license", "", "Go license")
	flags.StringVar(&inputs.GoPatents, "go-patents", "", "Go patents notice")
	flags.StringVar(&inputs.PklGoLicense, "pkl-go-license", "", "pkl-go license")
	flags.StringVar(&inputs.PklGoNotice, "pkl-go-notice", "", "pkl-go notice")
	flags.StringVar(&inputs.MsgpackLicense, "msgpack-license", "", "msgpack license")
	flags.StringVar(&inputs.TagparserLicense, "tagparser-license", "", "tagparser license")
	flags.StringVar(&inputs.YAMLLicense, "yaml-license", "", "yaml.v3 license")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || outputDirectory == "" {
		return fmt.Errorf("all inputs must be named flags and --output is required")
	}
	lock, err := distribution.LoadRuntimeLock(inputs.RuntimeLock)
	if err != nil {
		return err
	}
	if lock.WorkbenchVersion != inputs.Version {
		return fmt.Errorf("runtime lock Workbench version %q does not match release %q", lock.WorkbenchVersion, inputs.Version)
	}
	name, err := distribution.AssetName(inputs.Version, distribution.Platform{OS: goos, Arch: goarch})
	if err != nil {
		return err
	}
	archivePath := filepath.Join(outputDirectory, name)
	if err := distribution.WriteArchive(archivePath, inputs); err != nil {
		return err
	}
	contents, err := os.ReadFile(archivePath)
	if err != nil {
		return fmt.Errorf("read release archive for checksum: %w", err)
	}
	digest := sha256.Sum256(contents)
	encoded := hex.EncodeToString(digest[:])
	if err := os.WriteFile(archivePath+".sha256", []byte(encoded+"\n"), 0o644); err != nil {
		return fmt.Errorf("write release archive checksum: %w", err)
	}
	_, err = fmt.Fprintf(output, "%s\n", archivePath)
	return err
}
