package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"

	"github.com/seanrobmerriam/agentchaos/internal/event"
)

func cmdExplain(args []string) {
	fs := flag.NewFlagSet("explain", flag.ExitOnError)
	path := fs.String("event-log", "", "NDJSON event log produced by --event-log")
	fs.Parse(args)
	if *path == "" {
		fmt.Fprintln(os.Stderr, "explain: --event-log is required")
		os.Exit(1)
	}
	f, err := os.Open(*path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "explain: open %s: %v\n", *path, err)
		os.Exit(1)
	}
	defer f.Close()
	log := event.New()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if err := log.AppendJSONLine(sc.Bytes()); err != nil {
			fmt.Fprintf(os.Stderr, "explain: parse line: %v\n", err)
			os.Exit(1)
		}
	}
	if err := sc.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "explain: read %s: %v\n", *path, err)
		os.Exit(1)
	}
	event.PrintTimeline(os.Stdout, log.Events())
}
