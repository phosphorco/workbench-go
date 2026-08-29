package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"time"
)

type commandSpec struct {
	name string
	path string
	args []string
}

func main() {
	root := flag.String("root", "", "monorepo checkout")
	native := flag.String("native", "", "compiled native verdict helper")
	runs := flag.Int("runs", 40, "measured cold invocations per resolver")
	warmups := flag.Int("warmups", 5, "unmeasured invocations per resolver")
	flag.Parse()
	if *root == "" || *native == "" || *runs < 1 || *warmups < 0 {
		flag.Usage()
		os.Exit(2)
	}
	absoluteRoot, err := filepath.Abs(*root)
	if err != nil {
		fail(err)
	}
	commands := []commandSpec{
		{name: "go-native", path: *native, args: []string{absoluteRoot}},
		{
			name: "bun-typescript",
			path: "bun",
			args: []string{
				filepath.Join(absoluteRoot, "scripts/tsgo-artifact-contract.mts"),
				"check", "--consumer", "--repo-root", absoluteRoot,
			},
		},
	}
	verdicts := make([]bool, len(commands))
	for index, command := range commands {
		verdict, _, err := invoke(command, absoluteRoot)
		if err != nil {
			fail(err)
		}
		verdicts[index] = verdict
	}
	if verdicts[0] != verdicts[1] {
		fail(fmt.Errorf("resolver verdicts differ before measurement: native=%t bun=%t", verdicts[0], verdicts[1]))
	}
	for range *warmups {
		for _, command := range commands {
			if _, _, err := invoke(command, absoluteRoot); err != nil {
				fail(err)
			}
		}
	}
	samples := make([][]time.Duration, len(commands))
	for iteration := range *runs {
		order := []int{0, 1}
		if iteration%2 == 1 {
			order = []int{1, 0}
		}
		for _, index := range order {
			_, duration, err := invoke(commands[index], absoluteRoot)
			if err != nil {
				fail(err)
			}
			samples[index] = append(samples[index], duration)
		}
	}
	verdict := "refused"
	if verdicts[0] {
		verdict = "valid"
	}
	for index, command := range commands {
		sort.Slice(samples[index], func(left, right int) bool { return samples[index][left] < samples[index][right] })
		fmt.Printf("%s\tverdict=%s\truns=%d\tmin=%s\tmedian=%s\tp95=%s\n", command.name, verdict, *runs, samples[index][0], percentile(samples[index], 0.50), percentile(samples[index], 0.95))
	}
	nativeMedian := percentile(samples[0], 0.50)
	bunMedian := percentile(samples[1], 0.50)
	fmt.Printf("median-speedup\t%.2fx\n", float64(bunMedian)/float64(nativeMedian))
}

func invoke(spec commandSpec, root string) (bool, time.Duration, error) {
	command := exec.Command(spec.path, spec.args...)
	command.Dir = root
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	started := time.Now()
	err := command.Run()
	duration := time.Since(started)
	if err == nil {
		return true, duration, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, duration, nil
	}
	return false, duration, fmt.Errorf("invoke %s: %w", spec.name, err)
}

func percentile(samples []time.Duration, quantile float64) time.Duration {
	index := int(float64(len(samples)-1) * quantile)
	return samples[index]
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
