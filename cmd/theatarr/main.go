// Command theatarr is Theatarr, the media manager: one binary for the service and its tools.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
)

// command is one theatarr subcommand. Each lives in its own file and registers itself in init; a new subcommand also
// gets a heading in usageGroups below. A command with subcommands of its own (theatarr policy validate) parses them
// itself.
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

// usageGroups sorts the usage output under headings, in this order. A command missing here is still listed, under
// "other" (TestUsageGroupsEveryCommand keeps the list complete); the groups change only the help, never a name.
var usageGroups = []struct {
	title    string
	commands []string
}{
	{"run the service", []string{"serve", "healthcheck", "mcp", "admin", "backup", "version"}},
	{"check an install", []string{"preflight", "health", "egress-check", "policy", "errors", "jobs", "journal"}},
	{"migrate from the arrs", []string{"migrate", "import-arr", "settings", "shadow"}},
	{"debug and maintenance", []string{"debug", "decide", "search", "router", "providers", "library", "torrents", "anime",
		"tvdb", "sonarr-parse", "demo"}},
}

func usage() { writeUsage(os.Stderr) }

// writeUsage prints every command that is not hidden, under its usage group.
func writeUsage(w io.Writer) {
	fmt.Fprint(w, "usage: theatarr <command> [flags]\n")
	listed := map[string]bool{}
	section := func(title string, names []string) {
		var shown []string
		for _, n := range names {
			if c, ok := commands[n]; ok && !c.hidden && !listed[n] {
				shown = append(shown, n)
				listed[n] = true
			}
		}
		if len(shown) == 0 {
			return
		}
		fmt.Fprintf(w, "\n%s:\n", title)
		for _, n := range shown {
			fmt.Fprintf(w, "  %-14s %s\n", n, commands[n].summary)
		}
	}
	for _, g := range usageGroups {
		section(g.title, g.commands)
	}
	var rest []string
	for n := range commands {
		rest = append(rest, n)
	}
	sort.Strings(rest)
	section("other", rest)
}
