package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/phosphorco/workbench-go/internal/evaluate"
	"github.com/phosphorco/workbench-go/internal/plan"
	workbenchruntime "github.com/phosphorco/workbench-go/internal/runtime"
	"github.com/phosphorco/workbench-go/internal/version"
	contracts "github.com/phosphorco/workbench-go/pkl"
)

const planHelp = `workbench plan <verb> <file.plan.pkl> [flags]

Read (never runs oracles or writes the ledger):
  check       Check references, cycles, selectors, grants and graph connectivity.
  tick        Ready work, exact blockers, stale evidence and critical path.
  recall      Tick plus completed nodes and the full chronological event history.
  export      Complete snapshot as JSON by default; redirect stdout to save it.
  schema      Print the bundled Pkl contract; plans may amend "workbench:plan".

Write (append evidence or decisions to the sibling .ledger.jsonl):
  verify      Run --node's Mechanical oracle; omitted: all ready Guards.
  adjudicate  --node ID --result pass|fail --by OWNER --reason TEXT
  rule        --node ID --choice OPTION --by OWNER
  land        --node ID: record an artifact, independently of verification.
  grant       --node ID --paths PATH,PATH --reason TEXT
  revoke      --node ID --paths PATH,PATH --reason TEXT
  note        --text TEXT --by AUTHOR [--node ID]

Common flags:
  --plan FILE         Explicit alternative to the positional plan file.
  --ledger FILE       Override the inferred sibling ledger (missing means empty).
  --module-root DIR   Local Pkl import boundary (default: plan's directory).
  --cwd DIR           Mechanical oracle working directory (default: caller cwd).
  --timeout DURATION  Evaluation/command time budget (default: 30s).
  --node ID           Select one verification target, or name a write target.
  --by AUTHOR         Optional attribution for verify; required for rule/note/judgment.
  --format agent|json Agent context (default); export defaults to JSON.
  --help              Show this guide without loading a plan.

Output: XML-shaped context boundaries with compact Markdown, preserving literals.
These are reader boundaries, not parser-valid XML or an authority grant.
--format json: stable JSON, one node/event per line, from the same semantic report.
Oracle stdout/stderr goes to stderr. No TTY, color, prompts or agent dispatch.
Exit 0: successful command, including no ready work. Nonzero: invalid invocation,
invalid plan, unavailable tool, refused write, or a failed verification.
Read the result diagnostics and stderr to distinguish those outcomes.
Ready work is not assigned work; readiness never grants scope.
Pkl imports can read .pkl modules under --module-root; resources/env/network are denied.
Grants document task ownership; they do not sandbox the shell run by verify.

Examples:
  workbench plan tick feature.plan.pkl
  workbench plan tick feature.plan.pkl --format json | jq '.ready[].id'
  workbench plan verify feature.plan.pkl --node api --timeout 2m
  workbench plan recall feature.plan.pkl
`

type planInvocation struct {
	verb, file, ledger, moduleRoot, cwd, node, by, choice, result, reason, text, format string
	paths                                                                               []string
	timeout                                                                             time.Duration
}

var planVerbs = []string{"check", "tick", "recall", "export", "verify", "adjudicate", "rule", "land", "grant", "revoke", "note"}

func parsePlan(arguments []string) (planInvocation, error) {
	p := planInvocation{format: planOutputFormat(arguments)}
	p.timeout = 30 * time.Second
	if len(arguments) == 0 || !slices.Contains(planVerbs, arguments[0]) {
		return p, fmt.Errorf("unknown plan operation; run workbench plan --help for check, tick, verify and recording commands")
	}
	p.verb = arguments[0]
	values := map[string]string{}
	allowed := map[string]bool{"plan": true, "ledger": true, "module-root": true, "timeout": true, "format": true}
	for _, key := range map[string][]string{
		"verify": {"node", "by", "cwd"}, "adjudicate": {"node", "result", "by", "reason"}, "rule": {"node", "choice", "by"},
		"land": {"node"}, "grant": {"node", "paths", "reason"}, "revoke": {"node", "paths", "reason"}, "note": {"node", "text", "by"},
	}[p.verb] {
		allowed[key] = true
	}
	positional := false
	for i := 1; i < len(arguments); i++ {
		arg := arguments[i]
		if arg == "--" && !positional {
			positional = true
			continue
		}
		if !positional && strings.HasPrefix(arg, "-") {
			name, value, inline := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
			if !allowed[name] {
				return p, fmt.Errorf("plan %s does not accept %q; run workbench plan %s --help", p.verb, arg, p.verb)
			}
			if _, seen := values[name]; seen {
				return p, fmt.Errorf("--%s was supplied twice; provide one explicit value", name)
			}
			if !inline {
				i++
				if i >= len(arguments) {
					return p, fmt.Errorf("--%s requires a value", name)
				}
				value = arguments[i]
			}
			if value == "" {
				return p, fmt.Errorf("--%s requires a nonempty value", name)
			}
			values[name] = value
		} else {
			if p.file != "" {
				return p, fmt.Errorf("two plan files supplied; run one workbench plan %s FILE command per plan", p.verb)
			}
			p.file = arg
		}
	}
	if values["plan"] != "" {
		if p.file != "" {
			return p, fmt.Errorf("use a positional plan file or --plan, not both")
		}
		p.file = values["plan"]
	}
	if p.file == "" {
		return p, fmt.Errorf("plan file required; run workbench plan %s <file.plan.pkl>", p.verb)
	}
	if !strings.HasSuffix(p.file, ".plan.pkl") {
		return p, fmt.Errorf("%q is not a .plan.pkl definition; author Pkl with workbench plan schema", p.file)
	}
	if value := values["format"]; value != "" && value != "agent" && value != "json" {
		return p, fmt.Errorf("invalid --format %q; use --format agent or --format json", value)
	}
	p.ledger = values["ledger"]
	p.moduleRoot = values["module-root"]
	p.cwd = values["cwd"]
	p.node = values["node"]
	p.by = values["by"]
	p.choice = values["choice"]
	p.result = values["result"]
	p.reason = values["reason"]
	p.text = values["text"]
	if value := values["timeout"]; value != "" {
		duration, err := time.ParseDuration(value)
		if err != nil || duration <= 0 {
			return p, fmt.Errorf("invalid --timeout %q; use a positive duration such as --timeout 2m", value)
		}
		p.timeout = duration
	}
	if value := values["paths"]; value != "" {
		p.paths = strings.Split(value, ",")
		for _, path := range p.paths {
			if strings.TrimSpace(path) == "" {
				return p, fmt.Errorf("--paths contains an empty path")
			}
		}
	}
	required := map[string][]string{"adjudicate": {"node", "result", "by", "reason"}, "rule": {"node", "choice", "by"}, "land": {"node"}, "grant": {"node", "paths", "reason"}, "revoke": {"node", "paths", "reason"}, "note": {"text", "by"}}
	for _, key := range required[p.verb] {
		if strings.TrimSpace(values[key]) == "" {
			return p, fmt.Errorf("plan %s requires --%s; run workbench plan %s --help", p.verb, key, p.verb)
		}
	}
	if p.verb == "adjudicate" && p.result != "pass" && p.result != "fail" {
		return p, fmt.Errorf("--result must be pass or fail")
	}
	return p, nil
}

func runPlanCommand(ctx context.Context, arguments []string, workingDirectory func() (string, error), output, diagnostics io.Writer) error {
	if len(arguments) == 0 || slices.Contains(arguments, "--help") || arguments[0] == "help" {
		_, err := io.WriteString(output, planHelp)
		return err
	}
	if len(arguments) == 1 && arguments[0] == "schema" {
		_, err := io.WriteString(output, contracts.Plan)
		return err
	}
	p, err := parsePlan(arguments)
	if err != nil {
		return writePlanError(output, p.format, "Invocation", err)
	}
	root, err := workingDirectory()
	if err != nil {
		return writePlanError(output, p.format, "Environment", err)
	}
	resolve := func(path string) string {
		if filepath.IsAbs(path) {
			return filepath.Clean(path)
		}
		return filepath.Join(root, path)
	}
	p.file = resolve(p.file)
	if p.ledger == "" {
		p.ledger = strings.TrimSuffix(p.file, ".plan.pkl") + ".ledger.jsonl"
	} else {
		p.ledger = resolve(p.ledger)
	}
	if p.moduleRoot == "" {
		p.moduleRoot = filepath.Dir(p.file)
	} else {
		p.moduleRoot = resolve(p.moduleRoot)
	}
	if p.cwd == "" {
		p.cwd = root
	} else {
		p.cwd = resolve(p.cwd)
	}
	planInfo, planStatErr := os.Stat(p.file)
	ledgerInfo, ledgerStatErr := os.Stat(p.ledger)
	if p.file == p.ledger || planStatErr == nil && ledgerStatErr == nil && os.SameFile(planInfo, ledgerInfo) {
		return writePlanError(output, p.format, "Invocation", fmt.Errorf("plan and ledger must be different files"))
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	evaluator, err := planEvaluator()
	if err != nil {
		return writePlanError(output, p.format, "Environment", err)
	}
	if isPlanWrite(p.verb) {
		unlock, err := lockPlanLedger(p.ledger)
		if err != nil {
			return writePlanError(output, p.format, "LedgerBusy", err)
		}
		defer unlock()
	}
	definition, err := evaluator.EvaluatePlan(ctx, p.file, p.moduleRoot)
	if err != nil {
		return writePlanError(output, p.format, "Definition", err)
	}
	events, err := readPlanLedger(p.ledger)
	if err != nil {
		return writePlanError(output, p.format, "Ledger", err)
	}
	definition, err = legacyPlanFingerprints(ctx, definition, events)
	if err != nil {
		return writePlanError(output, p.format, "Compatibility", err)
	}
	report := plan.Tick(definition, events, p.verb == "recall" || p.verb == "export")
	report.Plan = p.file
	report.Ledger = p.ledger
	if len(report.Diagnostics) > 0 && p.verb != "revoke" && p.verb != "note" {
		if err := writePlanReport(output, p, report); err != nil {
			return err
		}
		return fmt.Errorf("plan has %d violation(s); repair the reported nodes in %s and rerun workbench plan check", len(report.Diagnostics), p.file)
	}
	if !isPlanWrite(p.verb) {
		return writePlanReport(output, p, report)
	}
	if p.verb == "verify" {
		return verifyPlan(ctx, p, definition, events, output, diagnostics)
	}
	event, err := planEvent(p, definition, events)
	if err != nil {
		return writePlanError(output, p.format, "Refused", err)
	}
	if event.Kind == "Granted" {
		proposed := append(slices.Clone(events), event)
		if violations := plan.Check(definition, plan.Fold(proposed)); len(violations) > 0 {
			return writePlanError(output, p.format, "Refused", fmt.Errorf("grant would violate %s: %s; narrow --paths before retrying", violations[0].Code, violations[0].Message))
		}
	}
	event, err = appendPlanEvent(p.ledger, event)
	if err != nil {
		return writePlanError(output, p.format, "Ledger", err)
	}
	return writePlanRecorded(output, p.format, planRecorded{p.file, p.ledger, event})
}

func planEvaluator() (evaluate.Evaluator, error) {
	if !version.IsDevelopment() {
		toolchain, err := workbenchruntime.Installed()
		if err != nil {
			return evaluate.Evaluator{}, err
		}
		return evaluate.NewEvaluator(toolchain.PklPath())
	}
	path, err := exec.LookPath("pkl")
	if err != nil {
		return evaluate.Evaluator{}, fmt.Errorf("Pkl is unavailable; run mise install pkl: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return evaluate.Evaluator{}, err
	}
	return evaluate.NewEvaluator(path)
}

func isPlanWrite(verb string) bool {
	return !slices.Contains([]string{"check", "tick", "recall", "export"}, verb)
}

func writePlanError(output io.Writer, format, code string, err error) error {
	if format != "json" {
		var body strings.Builder
		fmt.Fprintf(&body, "**%s:** %s\n", code, planLiteral(err.Error()))
		return errors.Join(err, writePlanAgent(output, body.String()))
	}
	writeErr := plan.WriteJSON(output, struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, err.Error()}})
	return errors.Join(err, writeErr)
}

func readPlanLedger(path string) ([]plan.Event, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return []plan.Event{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read ledger %s: %w", path, err)
	}
	return plan.DecodeLedger(data)
}

func lockPlanLedger(path string) (func(), error) {
	file, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open ledger lock: %w", err)
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("ledger %s is busy; retry this command after the current writer finishes: %w", path, err)
	}
	return func() { _ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN); _ = file.Close() }, nil
}

func appendPlanEvent(path string, event plan.Event) (plan.Event, error) {
	if err := event.Validate(); err != nil {
		return plan.Event{}, err
	}
	event.At = time.Now().UTC().Format(time.RFC3339Nano)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return plan.Event{}, err
	}
	var encoded bytes.Buffer
	if err := writeLedgerEvent(&encoded, event); err != nil {
		return plan.Event{}, errors.Join(err, file.Close())
	}
	info, writeErr := file.Stat()
	if writeErr == nil && info.Size() > 0 {
		var last [1]byte
		_, writeErr = file.ReadAt(last[:], info.Size()-1)
		if writeErr == nil && last[0] != '\n' {
			encodedWithPrefix := append([]byte("\n"), encoded.Bytes()...)
			encoded.Reset()
			encoded.Write(encodedWithPrefix)
		}
	}
	if writeErr == nil {
		_, writeErr = file.Write(encoded.Bytes())
	}
	if writeErr == nil {
		writeErr = file.Sync()
	}
	return event, errors.Join(writeErr, file.Close())
}

func planEvent(p planInvocation, d plan.Definition, events []plan.Event) (plan.Event, error) {
	e := plan.Event{Node: p.node, By: p.by, Reason: p.reason}
	n, found := d.Find(p.node)
	if p.node != "" && !found {
		return e, fmt.Errorf("node %q is absent; inspect ids with workbench plan recall %q", p.node, p.file)
	}
	s := plan.Fold(events)
	switch p.verb {
	case "note":
		e.Kind = "Note"
		e.Text = p.text
	case "land":
		if n.Kind == "Selector" {
			return e, fmt.Errorf("%s is a Selector; use workbench plan rule %q --node %q --choice OPTION --by %q", n.ID, p.file, n.ID, n.Owner)
		}
		e.Kind = "Artifact"
		e.Digest = d.Fingerprint(plan.Need{Kind: "Artifact", Node: n.ID})
	case "grant", "revoke":
		if n.Kind != "Action" {
			return e, fmt.Errorf("%s is a %s; grants require an Action", n.ID, n.Kind)
		}
		e.Kind = "Granted"
		if p.verb == "revoke" {
			e.Kind = "Revoked"
		}
		e.Paths = p.paths
	case "rule":
		if n.Kind != "Selector" {
			return e, fmt.Errorf("%s is not a Selector", n.ID)
		}
		if p.by != n.Owner {
			return e, fmt.Errorf("%s is decided by %q; record their decision with --by %q", n.ID, n.Owner, n.Owner)
		}
		if !slices.Contains(n.Options, p.choice) {
			return e, fmt.Errorf("%s choice %q is invalid; options: %s", n.ID, p.choice, strings.Join(n.Options, ", "))
		}
		if err := requirePlanDependencies(d, s, n); err != nil {
			return e, err
		}
		e.Kind = "ForkRuled"
		e.Choice = p.choice
		e.Digest = d.Fingerprint(n.Self())
	case "adjudicate":
		if n.Oracle == nil || n.Oracle.Kind != "Adjudicated" {
			return e, fmt.Errorf("%s needs Mechanical verification; run workbench plan verify %q --node %q", n.ID, p.file, n.ID)
		}
		if p.by != n.Oracle.Owner {
			return e, fmt.Errorf("%s is adjudicated by %q; record their judgment with --by %q", n.ID, n.Oracle.Owner, n.Oracle.Owner)
		}
		if err := requirePlanDependencies(d, s, n); err != nil {
			return e, err
		}
		e.Kind = "Evidence"
		e.Result = p.result
		e.Digest = d.Fingerprint(n.Self())
	default:
		return e, fmt.Errorf("unsupported write %s", p.verb)
	}
	return e, nil
}

func requirePlanDependencies(d plan.Definition, s plan.State, n plan.Node) error {
	for _, need := range n.Dependencies() {
		if !s.Satisfies(d, need) {
			return fmt.Errorf("%s is blocked by %s; inspect workbench plan tick before recording completion", n.ID, need.String())
		}
	}
	return nil
}

type planVerification struct {
	Node   string      `json:"node"`
	Result string      `json:"result"`
	Exit   int         `json:"exit"`
	Event  *plan.Event `json:"event,omitempty"`
}

func verifyPlan(ctx context.Context, p planInvocation, d plan.Definition, events []plan.Event, output, diagnostics io.Writer) error {
	s := plan.Fold(events)
	targets := []plan.Node{}
	if p.node != "" {
		n, ok := d.Find(p.node)
		if !ok {
			return writePlanError(output, p.format, "Refused", fmt.Errorf("unknown node %q; inspect workbench plan recall %q", p.node, p.file))
		}
		targets = append(targets, n)
	} else {
		for _, n := range d.Nodes {
			if n.Kind == "Guard" && n.Oracle.Kind == "Mechanical" && !s.Satisfies(d, n.Self()) && len(s.Missing(d, n)) == 0 {
				targets = append(targets, n)
			}
		}
	}
	for _, n := range targets {
		if n.Oracle == nil || n.Oracle.Kind != "Mechanical" {
			return writePlanError(output, p.format, "Refused", fmt.Errorf("%s has no Mechanical oracle; use rule for a Selector or adjudicate for a judgment", n.ID))
		}
		if err := requirePlanDependencies(d, s, n); err != nil {
			return writePlanError(output, p.format, "Refused", err)
		}
	}
	results := []planVerification{}
	var failure error
	for _, n := range targets {
		if n.Oracle.Once && s.Satisfies(d, n.Self()) {
			results = append(results, planVerification{Node: n.ID, Result: "already-observed"})
			continue
		}
		command := exec.CommandContext(ctx, "bash", "-c", n.Oracle.Run)
		command.Dir = p.cwd
		command.Stdout = diagnostics
		command.Stderr = diagnostics
		command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		command.Cancel = func() error {
			err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
			if errors.Is(err, syscall.ESRCH) {
				return os.ErrProcessDone
			}
			return err
		}
		command.WaitDelay = time.Second
		err := command.Run()
		code := 0
		result := "pass"
		if err != nil {
			var exit *exec.ExitError
			if ctx.Err() != nil || !errors.As(err, &exit) {
				failure = fmt.Errorf("oracle %s could not complete: %w", n.ID, errors.Join(err, ctx.Err()))
				results = append(results, planVerification{Node: n.ID, Result: "unobserved", Exit: -1})
				break
			}
			code = exit.ExitCode()
			result = "fail"
			failure = fmt.Errorf("oracle %s failed with exit %d", n.ID, code)
		}
		by := p.by
		if by == "" {
			by = "workbench"
		}
		event, appendErr := appendPlanEvent(p.ledger, plan.Event{Kind: "Evidence", Node: n.ID, Result: result, By: by, Digest: d.Fingerprint(n.Self())})
		if appendErr != nil {
			failure = errors.Join(failure, appendErr)
			break
		}
		results = append(results, planVerification{Node: n.ID, Result: result, Exit: code, Event: &event})
	}
	writeErr := writePlanVerified(output, p.format, planVerified{p.file, p.ledger, results})
	return errors.Join(failure, writeErr)
}

func writeLedgerEvent(w io.Writer, event plan.Event) error {
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(event)
}
