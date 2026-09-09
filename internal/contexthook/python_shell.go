package contexthook

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	"github.com/phosphorco/workbench-go/internal/contextapi"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// normalizePythonShell recognizes the adapter-owned shell boundary for the
// deliberately small Python intent subset. A false handled result leaves the
// existing shell detector responsible for non-Python commands.
func normalizePythonShell(
	call ToolCall,
	command, root, cwd string,
	maxWords, maxBytes int,
	now time.Time,
) ([]contextapi.ObservedResource, []contextapi.Reason, bool, error) {
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil || file == nil || len(file.Stmts) == 0 {
		return nil, nil, false, nil
	}

	firstStmt := file.Stmts[0]
	first, ok := firstStmt.Cmd.(*syntax.CallExpr)
	if !ok || len(first.Args) == 0 || len(first.Assigns) != 0 {
		return nil, nil, false, nil
	}
	interpreter, static := pythonShellWord(first.Args[0], maxBytes)
	if !static || !pythonInterpreter(interpreter) {
		return nil, nil, false, nil
	}
	if maxWords < 1 {
		maxWords = DefaultInputLimits().MaxShellWords
	}
	shellWords := len(first.Args)
	if shellWords > maxWords {
		return nil, nil, true, &SizeError{Operation: "shell words", Limit: int64(maxWords), Observed: int64(shellWords)}
	}

	// Once the first command is an approved Python interpreter, do not let the
	// legacy shell detector reinterpret an unsupported Python form as another
	// kind of shell attention.
	handled := true
	unsupported := func(summary string) ([]contextapi.ObservedResource, []contextapi.Reason, bool, error) {
		return nil, []contextapi.Reason{inferredUnknownReason(summary, now)}, handled, nil
	}

	if !pythonShellStmtFlagsAllowed(firstStmt, true) {
		return unsupported("python shell command has unsupported control flow or redirection")
	}
	for _, stmt := range file.Stmts[1:] {
		if !pythonShellStmtFlagsAllowed(stmt, false) {
			return unsupported("python shell command has unsupported trailing shell syntax")
		}
		trailing, ok := stmt.Cmd.(*syntax.CallExpr)
		if !ok || len(trailing.Assigns) != 0 || len(trailing.Args) == 0 {
			return unsupported("python shell command has unsupported trailing shell syntax")
		}
		shellWords += len(trailing.Args)
		if shellWords > maxWords {
			return nil, nil, true, &SizeError{Operation: "shell words", Limit: int64(maxWords), Observed: int64(shellWords)}
		}
		for _, arg := range trailing.Args {
			if _, static := pythonShellWord(arg, maxBytes); !static {
				return unsupported("python shell command has dynamic trailing shell arguments")
			}
		}
		name, static := pythonShellWord(trailing.Args[0], maxBytes)
		if static && name == "cd" {
			return unsupported("python shell command changes directory")
		}
	}

	args := make([]string, len(first.Args))
	for index, arg := range first.Args {
		value, static := pythonShellWord(arg, maxBytes)
		if !static {
			return unsupported("python shell command has dynamic arguments")
		}
		args[index] = value
	}

	workdir, valid := pythonEffectiveWorkdir(call.Name, call.Input, cwd, root)
	if !valid {
		return nil, []contextapi.Reason{invalidToolReason("python command workdir is invalid or outside the active repository", now)}, handled, nil
	}

	var source string
	if len(args) == 3 && args[1] == "-c" {
		if len(firstStmt.Redirs) != 0 {
			return unsupported("python -c command has unsupported redirection")
		}
		source = args[2]
	} else if len(args) == 2 && args[1] == "-" {
		if len(firstStmt.Redirs) != 1 || firstStmt.Redirs[0].Op != syntax.Hdoc || !pythonQuotedHeredoc(firstStmt.Redirs[0]) {
			return unsupported("python stdin command requires one fully quoted heredoc")
		}
		body, ok := pythonHeredocBody(firstStmt.Redirs[0].Hdoc, maxBytes)
		if !ok {
			return unsupported("python heredoc body is dynamic or oversized")
		}
		source = body
	} else {
		return unsupported("python command form is outside the supported static subset")
	}

	intents, complete := pythonFileIntents(source)
	if !complete {
		return unsupported("python source was not a supported static file-intent form")
	}
	resources := make([]contextapi.ObservedResource, 0, len(intents))
	reasons := make([]contextapi.Reason, 0)
	for _, intent := range intents {
		relative, ok := repositoryPath(intent.path, root, workdir)
		if !ok {
			reasons = append(reasons, invalidToolReason("python file intent escapes the active repository", now))
			continue
		}
		// Python parsing proves source-level intent only. In particular, do not
		// use nativeOutcome here: its result describes the outer shell call, not
		// an individual Python sink.
		resources = append(resources, contextapi.ObservedResource{
			Path: relative, Kind: contextapi.ResourceFile, Operation: intent.operation,
			Outcome: contextapi.ResourceOutcomeUnknown, Confidence: contextapi.ConfidenceInferred,
		})
	}
	return resources, reasons, handled, nil
}

func pythonInterpreter(name string) bool {
	if name == "python" || name == "python3" {
		return true
	}
	if !filepath.IsAbs(name) {
		return false
	}
	base := filepath.Base(name)
	return base == "python" || base == "python3"
}

func pythonShellStmtFlagsAllowed(stmt *syntax.Stmt, first bool) bool {
	if stmt == nil || stmt.Negated || stmt.Background || stmt.Coprocess || stmt.Disown || len(stmt.Redirs) > 0 && !first {
		return false
	}
	call, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Assigns) != 0 {
		return false
	}
	if first {
		return true
	}
	return len(stmt.Redirs) == 0
}

func pythonShellWord(word *syntax.Word, maxBytes int) (string, bool) {
	if word == nil {
		return "", false
	}
	if _, static := staticWord(word, maxBytes); !static || !pythonShellWordIsLiteral(word) {
		return "", false
	}
	cfg := expand.Config{Env: expand.FuncEnviron(func(string) string { return "" })}
	value, err := expand.Literal(&cfg, word)
	if err != nil || len([]byte(value)) > maxBytes || strings.IndexByte(value, 0) >= 0 {
		return "", false
	}
	return value, true
}

func pythonShellWordIsLiteral(word *syntax.Word) bool {
	literal := true
	for _, part := range word.Parts {
		if lit, ok := part.(*syntax.Lit); ok {
			// expand.Literal performs tilde expansion before consulting Env. An
			// empty environment still permits os/user.Lookup for ~user, so
			// reject every unquoted tilde before invoking it.
			if strings.Contains(lit.Value, "~") {
				return false
			}
		}
	}
	syntax.Walk(word, func(node syntax.Node) bool {
		switch node := node.(type) {
		case *syntax.SglQuoted:
			literal = literal && !node.Dollar
		case *syntax.DblQuoted:
			literal = literal && !node.Dollar
		}
		return literal
	})
	return literal
}

func pythonQuotedHeredoc(redirect *syntax.Redirect) bool {
	if redirect == nil || redirect.N != nil || redirect.Word == nil || len(redirect.Word.Parts) == 0 || redirect.Op != syntax.Hdoc {
		return false
	}
	for _, part := range redirect.Word.Parts {
		switch part := part.(type) {
		case *syntax.SglQuoted:
			if part.Dollar {
				return false
			}
		case *syntax.DblQuoted:
			if part.Dollar {
				return false
			}
			for _, nested := range part.Parts {
				if _, ok := nested.(*syntax.Lit); !ok {
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

func pythonHeredocBody(word *syntax.Word, maxBytes int) (string, bool) {
	if word == nil {
		return "", true
	}
	var body strings.Builder
	for _, part := range word.Parts {
		lit, ok := part.(*syntax.Lit)
		if !ok {
			return "", false
		}
		if body.Len()+len(lit.Value) > maxBytes {
			return "", false
		}
		body.WriteString(lit.Value)
	}
	// A quoted here-document delimiter disables all here-document
	// processing. Its body is therefore already the exact bytes delivered to
	// stdin; do not run shell quote removal or expansion over it.
	return body.String(), true
}

func pythonEffectiveWorkdir(toolName string, input json.RawMessage, cwd, root string) (string, bool) {
	if strings.IndexByte(cwd, 0) >= 0 {
		return "", false
	}
	workdir := cwd
	if pythonWorkdirTool(toolName) {
		var object struct {
			Workdir json.RawMessage `json:"workdir"`
		}
		if json.Unmarshal(input, &object) != nil {
			return "", false
		}
		if object.Workdir != nil {
			var value string
			if json.Unmarshal(object.Workdir, &value) != nil || value == "" {
				return "", false
			}
			if strings.IndexByte(value, 0) >= 0 {
				return "", false
			}
			if filepath.IsAbs(value) {
				workdir = filepath.Clean(value)
			} else {
				workdir = filepath.Clean(filepath.Join(cwd, value))
			}
		}
	}
	if strings.IndexByte(workdir, 0) >= 0 {
		return "", false
	}
	if !filepath.IsAbs(workdir) || !pythonPathWithinRoot(workdir, root) {
		return "", false
	}
	return filepath.Clean(workdir), true
}

func pythonWorkdirTool(name string) bool {
	switch strings.ToLower(name) {
	case "exec_command", "unified_exec", "exec", "shell":
		return true
	default:
		return false
	}
}

func pythonPathWithinRoot(candidate, root string) bool {
	if candidate == "" || root == "" || !filepath.IsAbs(candidate) || !filepath.IsAbs(root) {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) && !filepath.IsAbs(relative)
}
