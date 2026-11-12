package main

import (
	"strings"
	"testing"
)

// A parent command's usage (theatarr backup -h, or no subcommand) lists the subcommands without a "flags:" header
// over nothing; the subcommands' -h adds the header with their flags.
func TestParentUsageHasNoEmptyFlagsHeader(t *testing.T) {
	for name, usage := range map[string]string{"backup": backupUsage, "debug": debugUsage,
		"errors": errorsUsage, "library": libraryUsage, "jobs": jobsUsage, "router": routerUsage, "shadow": shadowUsage} {
		if strings.HasSuffix(strings.TrimSpace(usage), "flags:") || strings.HasSuffix(usage, "\n\n") {
			t.Errorf("%s usage ends with %q", name, usage[max(0, len(usage)-20):])
		}
	}
}
