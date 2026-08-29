package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/phosphorco/workbench-go/internal/buildable"
	tsgospike "github.com/phosphorco/workbench-go/pkl-spike/tsgo"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: tsgo-verdict <repository-root>")
		os.Exit(2)
	}
	_, err := buildable.ResolveDeclared(context.Background(), os.Args[1], "tsgo", tsgospike.Definition(), "linux", "amd64")
	if err == nil {
		return
	}
	var refusal *buildable.Refusal
	if !errors.As(err, &refusal) {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(1)
}
