package main

import (
	"fmt"
	"os"

	"github.com/CNife/flyer/internal/cli"
)

func main() {
	if err := cli.Run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "flyer:", err)
		os.Exit(1)
	}
}
