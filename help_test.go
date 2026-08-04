package main

import (
	"slices"
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

func TestManAliasesHelp(t *testing.T) {
	man, ok := applets["man"]
	if !ok {
		t.Fatal("man applet is not registered")
	}

	helpStatus, helpOut, helpErr := captureApplet(t, cmdHelp, []string{"cat"}, "")
	manStatus, manOut, manErr := captureApplet(t, man, []string{"cat"}, "")
	if manStatus != helpStatus || manOut != helpOut || manErr != helpErr {
		t.Fatalf("help=(%d,%q,%q) man=(%d,%q,%q)",
			helpStatus, helpOut, helpErr, manStatus, manOut, manErr)
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

func TestParseHardeningOptions(t *testing.T) {
	tests := []struct {
		args    []string
		rest    []string
		enabled bool
	}{
		{args: []string{"cat", "file"}, rest: []string{"cat", "file"}, enabled: true},
		{args: []string{"--no-seccomp", "cat", "file"}, rest: []string{"cat", "file"}, enabled: false},
		{args: []string{"--seccomp=off", "cat"}, rest: []string{"cat"}, enabled: false},
		{args: []string{"--no-seccomp", "--seccomp=on", "cat"}, rest: []string{"cat"}, enabled: true},
	}
	for _, test := range tests {
		rest, enabled, err := parseHardeningOptions(test.args)
		if err != nil {
			t.Fatalf("parseHardeningOptions(%q): %v", test.args, err)
		}
		if !slices.Equal(rest, test.rest) || enabled != test.enabled {
			t.Fatalf("parseHardeningOptions(%q)=(%q,%v), want (%q,%v)", test.args, rest, enabled, test.rest, test.enabled)
		}
	}
	if _, _, err := parseHardeningOptions([]string{"--seccomp=maybe", "cat"}); err == nil {
		t.Fatal("invalid seccomp mode was accepted")
	}
}
