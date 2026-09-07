package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/phosphorco/workbench-go/internal/plan"
	workbenchruntime "github.com/phosphorco/workbench-go/internal/runtime"
	"github.com/phosphorco/workbench-go/internal/version"
)

// Only historical Bun.hash fingerprints use this adapter. It evaluates a fixed
// hash expression over inert data, never imports or executes an authored plan.
func legacyPlanFingerprints(ctx context.Context, d plan.Definition, events []plan.Event) (plan.Definition, error) {
	if len(plan.LegacyDigests(events)) == 0 {
		return d, nil
	}
	bun := ""
	if version.IsDevelopment() {
		path, err := exec.LookPath("bun")
		if err != nil {
			return d, fmt.Errorf("legacy ledger fingerprints require Bun; run mise install bun: %w", err)
		}
		bun = path
	} else {
		toolchain, err := workbenchruntime.Installed()
		if err != nil {
			return d, err
		}
		bun = toolchain.BunPath()
	}
	type input struct {
		Parts []string `json:"parts"`
		Sort  bool     `json:"sort"`
	}
	inputs := []input{}
	needs := []plan.Need{}
	for _, n := range d.Nodes {
		for _, kind := range []string{"Artifact", "Evidence", "Fork"} {
			if kind == "Fork" && n.Kind != "Selector" || kind != "Fork" && n.Kind == "Selector" {
				continue
			}
			need := plan.Need{Kind: kind, Node: n.ID}
			needs = append(needs, need)
			inputs = append(inputs, input{Parts: d.FingerprintParts(need), Sort: kind != "Evidence"})
		}
	}
	encoded, err := json.Marshal(inputs)
	if err != nil {
		return d, err
	}
	command := exec.CommandContext(ctx, bun, "-e", `const values=await Bun.stdin.json(); process.stdout.write(JSON.stringify(values.map(v=>Bun.hash(JSON.stringify(v.sort?v.parts.sort():v.parts)).toString(36))))`)
	command.Stdin = bytes.NewReader(encoded)
	output, err := command.Output()
	if err != nil {
		return d, fmt.Errorf("read legacy plan fingerprints: %w", err)
	}
	var digests []string
	if err := json.Unmarshal(output, &digests); err != nil {
		return d, fmt.Errorf("decode legacy fingerprints: %w", err)
	}
	if len(digests) != len(needs) {
		return d, fmt.Errorf("legacy fingerprint result count does not match the plan")
	}
	values := make(map[plan.Need]string, len(needs))
	for i, need := range needs {
		values[need] = digests[i]
	}
	return d.WithLegacyFingerprints(values), nil
}
