package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "run":
		cmdRun(os.Args[2:])
	case "replay":
		cmdReplay(os.Args[2:])
	case "validate":
		cmdValidate(os.Args[2:])
	case "inspect":
		cmdInspect(os.Args[2:])
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `agentchaos — fault injection proxy for MCP workflows

Usage:
  agentchaos run      --scenario <path> [--upstream <cmd>] [--seeds N] [--shrink-on-failure]
  agentchaos replay   --seed <uint64> --scenario <path> [--upstream <cmd>]
  agentchaos validate --scenario <path>
  agentchaos inspect  --scenario <path>`)
}
