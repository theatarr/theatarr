// Command theatarr is Theatarr, the media manager: one binary for the service and its tools.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
)

// command is one theatarr subcommand. Each lives in its own file and registers itself in init, so adding a subcommand
// never touches this file. A command with subcommands of its own (theatarr policy validate) parses them itself.
type command struct {
	name    string
	summary string
	run     func(args []string) error
	hidden  bool // left out of the usage: run by theatarr itself
}

var commands = map[string]command{}

func register(c command) {
	if _, dup := commands[c.name]; dup {
		panic("theatarr: duplicate command " + c.name)
	}
	commands[c.name] = c
}

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "-h", "-help", "--help", "help":
		usage()
		return
	}
	c, ok := commands[os.Args[1]]
	if !ok {
		fmt.Fprintf(os.Stderr, "theatarr: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err := c.run(os.Args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return // -h: the command printed its usage
		}
		fmt.Fprintf(os.Stderr, "theatarr %s: %v\n", c.name, err)
		os.Exit(1)
	}
}

// helpArg reports whether a command with subcommands is asked for its usage (-h, -help or --help in place of the
// subcommand): it prints the usage and returns flag.ErrHelp, which main does not report as an error.
func helpArg(args []string) bool {
	return len(args) > 0 && (args[0] == "-h" || args[0] == "-help" || args[0] == "--help")
}

func usage() {
	names := make([]string, 0, len(commands))
	for n, c := range commands {
		if !c.hidden {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	fmt.Fprint(os.Stderr, "usage: theatarr <command> [flags]\n\ncommands:\n")
	for _, n := range names {
		fmt.Fprintf(os.Stderr, "  %-14s %s\n", n, commands[n].summary)
	}
}
