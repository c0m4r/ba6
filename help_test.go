package main

import (
	"strings"
	"testing"
)

func TestHelpCommandAndFlagShareContent(t *testing.T) {
	statusCommand, commandOut, commandErr := captureApplet(t, cmdHelp, []string{"cat"}, "")
	statusFlag, flagOut, flagErr := captureApplet(t, func(args []string) int {
		return runApplet("cat", cmdCat, args)
	}, []string{"--help"}, "")
	if statusCommand != 0 || statusFlag != 0 || commandOut != flagOut {
		t.Fatalf("command=(%d,%q,%q) flag=(%d,%q,%q)",
			statusCommand, commandOut, commandErr, statusFlag, flagOut, flagErr)
	}
	if !strings.Contains(commandOut, "Usage: cat") || !strings.Contains(commandOut, "-n") {
		t.Fatalf("incomplete cat help: %q", commandOut)
	}
}

func TestDoubleDashPreventsHelpInterception(t *testing.T) {
	if helpRequested([]string{"--", "--help"}) {
		t.Fatal("literal --help operand was intercepted")
	}
}

func TestEveryAppletHasHelp(t *testing.T) {
	for name := range applets {
		if _, ok := appletHelp[name]; !ok {
			t.Errorf("missing help for %s", name)
		}
	}
}
