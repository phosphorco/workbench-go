// Package skills renders independently installable folders from embedded sources.
package skills

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"regexp"
	"strings"
	"text/template"

	"github.com/phosphorco/workbench-go/internal/skills"
	"github.com/phosphorco/workbench-go/internal/version"
	contracts "github.com/phosphorco/workbench-go/pkl"
)

//go:embed all:workbench all:workbench-plan
var sources embed.FS

type Parameters struct{ PklVersion string }

// Render validates the entire rendered folder catalog using the same loader
// as repository-authored skills. Markdown is templated; other assets retain bytes.
func Render(parameters Parameters) (skills.Catalog, error) {
	if parameters.PklVersion == "" {
		parameters.PklVersion = contracts.PackageVersion
	}
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(parameters.PklVersion) {
		return skills.Catalog{}, fmt.Errorf("invalid Pkl version %q; use an exact version such as --pkl-package-version %s", parameters.PklVersion, contracts.PackageVersion)
	}
	uri := fmt.Sprintf("package://github.com/phosphorco/workbench-go/releases/download/%s/workbench@%s", version.PackageReleaseCoordinate(parameters.PklVersion), parameters.PklVersion)
	return skills.Load([]skills.Source{{Name: "workbench-bundled", Root: ".", FS: renderedSource{FS: sources, packageURI: uri}}})
}

type renderedSource struct {
	fs.FS
	packageURI string
}

func (s renderedSource) Open(name string) (fs.File, error) {
	file, err := s.FS.Open(name)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(name, ".md") {
		return file, nil
	}
	contents, readErr := io.ReadAll(file)
	info, statErr := file.Stat()
	closeErr := file.Close()
	if readErr != nil {
		return nil, readErr
	}
	if statErr != nil {
		return nil, statErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	parsed, err := template.New(name).Option("missingkey=error").Parse(string(contents))
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := parsed.Execute(&output, struct{ PklPackageURI string }{s.packageURI}); err != nil {
		return nil, err
	}
	return &renderedFile{Reader: bytes.NewReader(output.Bytes()), info: renderedInfo{FileInfo: info, size: int64(output.Len())}}, nil
}

type renderedFile struct {
	*bytes.Reader
	info fs.FileInfo
}

func (f *renderedFile) Stat() (fs.FileInfo, error) { return f.info, nil }
func (f *renderedFile) Close() error               { return nil }

type renderedInfo struct {
	fs.FileInfo
	size int64
}

func (i renderedInfo) Size() int64 { return i.size }
