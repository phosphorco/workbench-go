package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/phosphorco/workbench-go/internal/contract"
	"github.com/phosphorco/workbench-go/internal/skills"
	contracts "github.com/phosphorco/workbench-go/pkl"
	bundledskills "github.com/phosphorco/workbench-go/skills"
)

const selfHelp = `workbench self skills <command>

  list           List bundled skills and their export commands. Read-only JSON.
  export <name>  Create a complete skill folder, including references and assets.

Bundled skills:
  workbench       Environment preparation and maintenance.
  workbench-plan  Optional Pkl task graphs and evidence recording.

Export options (accepted before or after the name):
  --skills-dir DIR             Parent directory (default: .agents/skills).
  --pkl-package-version X.Y.Z  Version used in Pkl package reference URLs.
                              Defaults to the bundled contract's package version.
                              Does not select the Pkl runtime or fetch a release.
  -h, --help                  Show help without exporting.

Export preserves existing skill folders. To compare an update, export into a
fresh directory. Each skill is independent; exporting one leaves others alone.

List and export write JSON to stdout; errors go to stderr. No runtime or network
is required. Exit 0: success/help; 2: invalid invocation; 3: destination exists;
1: operational failure (an incomplete folder, if any, is named in the error).

Examples:
  workbench self skills list
  workbench self skills export workbench-plan
  workbench self skills export workbench-plan --skills-dir="$HOME/.claude/skills"
`

type selfError struct {
	status int
	err    error
}

func (e selfError) Error() string                { return e.err.Error() }
func (e selfError) Unwrap() error                { return e.err }
func (e selfError) ExitCode() int                { return e.status }
func selfUsage(format string, args ...any) error { return selfError{2, fmt.Errorf(format, args...)} }

func runSelfCommand(arguments []string, workingDirectory func() (string, error), output io.Writer) error {
	help := func() error { _, err := io.WriteString(output, selfHelp); return err }
	if len(arguments) == 0 || len(arguments) == 1 && isSelfHelp(arguments[0]) {
		return help()
	}
	if arguments[0] == "skill" {
		corrected := append([]string{"skills", "export"}, arguments[1:]...)
		for i, arg := range corrected {
			if arg == "--pkl-version" || strings.HasPrefix(arg, "--pkl-version=") {
				corrected[i] = strings.Replace(arg, "--pkl-version", "--pkl-package-version", 1)
			}
		}
		if len(arguments) == 1 {
			corrected = []string{"skills", "list"}
		}
		return selfUsage("skill export now has an explicit action; run %s", selfCommand(corrected))
	}
	if arguments[0] != "skills" {
		return selfUsage("unknown self command %q; run workbench self skills list", arguments[0])
	}
	if len(arguments) == 1 || len(arguments) == 2 && isSelfHelp(arguments[1]) {
		return help()
	}
	switch arguments[1] {
	case "list":
		if len(arguments) == 3 && isSelfHelp(arguments[2]) {
			return help()
		}
		if len(arguments) != 2 {
			return selfUsage("list takes no arguments and already emits JSON; run workbench self skills list")
		}
		catalog, err := bundledskills.Render(bundledskills.Parameters{})
		if err != nil {
			return err
		}
		selected, err := skills.Select(catalog, contract.SkillSelection{All: true})
		if err != nil {
			return err
		}
		type item struct {
			Name          string `json:"name"`
			Description   string `json:"description"`
			ExportCommand string `json:"exportCommand"`
		}
		items := make([]item, 0, len(selected))
		for _, skill := range selected {
			items = append(items, item{skill.Name, skill.Description, "workbench self skills export " + skill.Name})
		}
		return writeJSONReport(output, struct {
			Skills []item `json:"skills"`
		}{items})
	case "export":
		return exportSelfSkill(arguments, workingDirectory, output)
	default:
		corrected := append([]string(nil), arguments...)
		switch arguments[1] {
		case "ls", "lists", "listt":
			corrected[1] = "list"
		case "exportt", "exprot":
			corrected[1] = "export"
		default:
			return selfUsage("unknown skills command %q; run workbench self skills list or workbench self skills export workbench-plan", arguments[1])
		}
		return selfUsage("unknown skills command %q; run %s", arguments[1], selfCommand(corrected))
	}
}

func exportSelfSkill(arguments []string, workingDirectory func() (string, error), output io.Writer) error {
	name := ""
	values := map[string]string{}
	positional := false
	for i := 2; i < len(arguments); i++ {
		arg := arguments[i]
		if !positional && isSelfHelp(arg) {
			_, err := io.WriteString(output, selfHelp)
			return err
		}
		if !positional && arg == "--" {
			positional = true
			continue
		}
		if positional || !strings.HasPrefix(arg, "-") {
			if name != "" {
				return selfUsage("export accepts one skill name; run workbench self skills list to choose a skill")
			}
			name = arg
			continue
		}
		flag, value, inline := strings.Cut(arg, "=")
		if flag != "--skills-dir" && flag != "--pkl-package-version" {
			correction := ""
			switch flag {
			case "--skills-dire", "--skill-dir", "--skills-folder", "--output":
				correction = "--skills-dir"
			case "--pkl-version", "--pkl-package-verison":
				correction = "--pkl-package-version"
			}
			if correction != "" {
				corrected := append([]string(nil), arguments...)
				corrected[i] = strings.Replace(arg, flag, correction, 1)
				return selfUsage("unknown option %q; run %s", flag, selfCommand(corrected))
			}
			if flag == "--force" || flag == "--overwrite" {
				if name == "" {
					return selfUsage("export preserves existing folders; run workbench self skills export --help for exporting a comparison copy")
				}
				return selfUsage("export preserves existing folders; run %s --skills-dir \"$(mktemp -d)\" to compare a fresh export", selfCommand([]string{"skills", "export", name}))
			}
			return selfUsage("unknown export option %q; run workbench self skills export --help for --skills-dir and --pkl-package-version", flag)
		}
		if _, exists := values[flag]; exists {
			return selfUsage("%s was supplied twice; provide one value; run workbench self skills export --help", flag)
		}
		if !inline {
			i++
			if i >= len(arguments) || strings.HasPrefix(arguments[i], "--") {
				return selfUsage("%s requires a value; run workbench self skills export --help", flag)
			}
			value = arguments[i]
		}
		if strings.TrimSpace(value) == "" {
			return selfUsage("%s requires a nonempty value; run workbench self skills export --help", flag)
		}
		values[flag] = value
	}
	if name != "workbench" && name != "workbench-plan" {
		if name == "" {
			return selfUsage("export requires a skill name; run workbench self skills export workbench or workbench self skills export workbench-plan")
		}
		return selfUsage("unknown bundled skill %q; run workbench self skills export workbench or workbench self skills export workbench-plan", name)
	}
	skillsDir := values["--skills-dir"]
	if skillsDir == "" {
		skillsDir = ".agents/skills"
	}
	packageVersion := values["--pkl-package-version"]
	if packageVersion == "" {
		packageVersion = contracts.PackageVersion
	}
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(packageVersion) {
		return selfUsage("invalid Pkl package version %q; run %s", packageVersion, selfCommand([]string{"skills", "export", name, "--skills-dir", skillsDir, "--pkl-package-version", contracts.PackageVersion}))
	}
	catalog, err := bundledskills.Render(bundledskills.Parameters{PklVersion: packageVersion})
	if err != nil {
		return err
	}
	selected, err := skills.Select(catalog, contract.SkillSelection{Names: []string{name}})
	if err != nil {
		return err
	}
	destination := filepath.Clean(skillsDir)
	if !filepath.IsAbs(destination) {
		root, err := workingDirectory()
		if err != nil {
			return fmt.Errorf("resolve skill export directory: %w", err)
		}
		destination = filepath.Join(root, destination)
	}
	paths, err := skills.Export(destination, selected)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return selfError{3, fmt.Errorf("%w; compare a fresh export with: workbench self skills export %s --skills-dir \"$(mktemp -d)\" --pkl-package-version %s", err, name, packageVersion)}
		}
		return err
	}
	return writeJSONReport(output, struct {
		Skill             string   `json:"skill"`
		Directory         string   `json:"directory"`
		PklPackageVersion string   `json:"pklPackageVersion"`
		Files             []string `json:"files"`
	}{name, filepath.Join(destination, name), packageVersion, paths})
}

func isSelfHelp(argument string) bool {
	return argument == "--help" || argument == "-h" || argument == "help"
}
func selfCommand(arguments []string) string {
	words := []string{"workbench", "self"}
	for _, arg := range arguments {
		if arg != "" && strings.IndexFunc(arg, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./:=@+-", r))
		}) < 0 {
			words = append(words, arg)
		} else {
			words = append(words, "'"+strings.ReplaceAll(arg, "'", "'\"'\"'")+"'")
		}
	}
	return strings.Join(words, " ")
}
