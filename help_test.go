package main

import (
	"os/exec"
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

func TestBashCompletionGeneration(t *testing.T) {
	status, output, stderr := captureApplet(t, cmdCompletion, []string{"bash"}, "")
	if status != 0 || stderr != "" {
		t.Fatalf("completion bash = (%d, %q)", status, stderr)
	}
	for _, fragment := range []string{"complete -F _ba6_complete ba6", "'completion'", "'mtr'", "-4", "--help"} {
		if !strings.Contains(output, fragment) {
			t.Errorf("completion output is missing %q", fragment)
		}
	}

	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is unavailable")
	}
	command := exec.Command(bash, "-n") //nolint:gosec // The resolved executable is specifically Bash, with fixed arguments.
	command.Stdin = strings.NewReader(output)
	if diagnostic, err := command.CombinedOutput(); err != nil {
		t.Fatalf("generated completion is invalid Bash: %v: %s", err, diagnostic)
	}
}

func TestBashCompletionRejectsUnknownShell(t *testing.T) {
	status, _, stderr := captureApplet(t, cmdCompletion, []string{"zsh"}, "")
	if status != 2 || !strings.Contains(stderr, "completion bash") {
		t.Fatalf("completion zsh = (%d, %q)", status, stderr)
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
