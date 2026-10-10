package client

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

type Options struct {
	Focus      Focus
	FocusNames []string
	Gate       *string
	All        bool
	Reset      bool
	JSON       bool
	Home       string
	Session    string
	Help       bool
}

type UsageError struct{ Message string }

func (e *UsageError) Error() string { return e.Message }
func (e *UsageError) ExitCode() int { return 2 }

// Parse returns plain options; it neither evaluates policy nor changes storage.
func Parse(argv []string, env map[string]string) (Options, error) {
	o := Options{Focus: EmptyFocus(), FocusNames: []string{}, Home: env["VERIFY_HOME"], Session: env["PHOSPHOR_VERIFY_SESSION"]}
	if o.Session == "" {
		o.Session = env["BB_THREAD_ID"]
	}
	if o.Session == "" {
		o.Session = "default"
	}
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		switch arg {
		case "--all":
			o.All = true
		case "--reset":
			o.Reset = true
		case "--json":
			o.JSON = true
		case "--help", "-h":
			o.Help = true
			return o, nil
		case "--gate", "--home", "--session":
			i++
			if i == len(argv) {
				return o, &UsageError{arg + " needs a value"}
			}
			v := argv[i]
			switch arg {
			case "--gate":
				o.Gate = &v
			case "--home":
				o.Home = v
			case "--session":
				o.Session = v
			}
		default:
			kind, rest, ok := strings.Cut(arg, ":")
			if !ok || rest == "" {
				return o, &UsageError{"unrecognized argument " + quote(arg)}
			}
			switch kind {
			case "file":
				o.Focus.Files = append(o.Focus.Files, rest)
			case "code":
				o.Focus.Codes = append(o.Focus.Codes, nonempty(rest)...)
			case "verifier":
				o.Focus.Verifiers = append(o.Focus.Verifiers, nonempty(rest)...)
			case "unit":
				o.Focus.Units = append(o.Focus.Units, rest)
			case "member":
				o.Focus.Members = append(o.Focus.Members, rest)
			case "focus":
				o.FocusNames = append(o.FocusNames, rest)
			case "severity":
				for _, v := range strings.Split(rest, ",") {
					if !slices.Contains([]string{"error", "warning", "suggestion", "message"}, v) {
						return o, &UsageError{"unknown severity " + quote(v)}
					}
					o.Focus.Severities = append(o.Focus.Severities, v)
				}
			default:
				return o, &UsageError{"unrecognized argument " + quote(arg)}
			}
		}
	}
	if o.Home == "" {
		home, err := os.UserHomeDir()
		if configured, exists := env["HOME"]; exists {
			home = configured
			err = nil
		}
		if err != nil {
			return o, err
		}
		o.Home = filepath.Join(home, ".verify")
	}
	abs, err := filepath.Abs(o.Home)
	o.Home = abs
	return o, err
}
func quote(s string) string { b, _ := encode(s, false); return string(b) }
func nonempty(s string) []string {
	out := []string{}
	for _, v := range strings.Split(s, ",") {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}
func ResolveFocus(o Options, declared map[string]Focus) (Focus, error) {
	f := o.Focus
	for _, name := range o.FocusNames {
		named, ok := declared[name]
		if !ok {
			return f, &UsageError{fmt.Sprintf("no focus %s in workbench.pkl", quote(name))}
		}
		f = Merge(f, named)
	}
	return f, nil
}
