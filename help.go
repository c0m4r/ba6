package main

import (
	"fmt"
	"io"
	"os"
	"sort"
)

var appletHelp = map[string]string{ //nolint:gosec // G101: command help contains words such as "prefix", not credentials.
	"[": `Usage: [ EXPRESSION ]
Evaluate a conditional expression. See "test --help" for operators.`,
	"basename": `Usage: basename NAME [SUFFIX]
       basename OPTION... NAME...
Print NAME with leading directory components removed.

Options:
  -a        support multiple NAME operands
  -s SUFFIX remove a trailing SUFFIX; implies -a
  --help    show this help`,
	"chgrp": `Usage: chgrp [OPTION]... GROUP FILE...
Change the group of each FILE. GROUP may be a name or numeric ID.

Options:
  -R        operate recursively
  -h        affect symbolic links instead of their referents
  --help    show this help`,
	"chmod": `Usage: chmod [OPTION]... OCTAL_MODE FILE...
Change file permissions using an octal mode from 0000 through 7777.

Options:
  -R        operate recursively
  --help    show this help`,
	"chown": `Usage: chown [OPTION]... OWNER[:GROUP] FILE...
Change file ownership. OWNER and GROUP may be names or numeric IDs.

Options:
  -R        operate recursively
  -h        affect symbolic links instead of their referents
  --help    show this help`,
	"date": `Usage: date [OPTION]... [+FORMAT]
Display the current time; this applet does not set the system clock.

Options:
  -u        use UTC
  -r FILE   display FILE's modification time
  --help    show this help

FORMAT accepts common strftime directives including %F, %T, %Y, %m, %d,
%H, %M, %S, %s, %N, %z, and %Z.`,
	"dirname": `Usage: dirname NAME...
Print each NAME with its last non-slash component removed.

Options:
  --help    show this help`,
	"false": `Usage: false
Return an unsuccessful status.

Options:
  --help    show this help`,
	"ln": `Usage: ln [OPTION]... TARGET [LINK_NAME]
       ln [OPTION]... TARGET... DIRECTORY
Create hard links by default, or symbolic links with -s.

Options:
  -s        make symbolic links
  -f        remove existing non-directory destinations
  -T        always treat the last operand as a link name
  -v        print each link as it is created
  --help    show this help`,
	"readlink": `Usage: readlink [OPTION] FILE
Print the value of a symbolic link.

Options:
  -f        canonicalize by following every symbolic link
  --help    show this help`,
	"realpath": `Usage: realpath FILE...
Print canonical absolute paths. Every path component must exist.

Options:
  --help    show this help`,
	"sleep": `Usage: sleep NUMBER[SUFFIX]...
Pause for the combined duration of all operands.

SUFFIX may be s for seconds (default), m for minutes, h for hours, or d for days.

Options:
  --help    show this help`,
	"stat": `Usage: stat [OPTION]... FILE...
Display file metadata.

Options:
  -L        follow symbolic links
  -c FORMAT use FORMAT instead of the default display
  --help    show this help

Common FORMAT sequences include %n, %N, %s, %a, %A, %u, %U, %g, %G,
%i, %h, %F, %x, %y, %z, %X, %Y, and %Z.`,
	"tee": `Usage: tee [OPTION]... [FILE]...
Copy standard input to standard output and each FILE.

Options:
  -a        append instead of overwriting
  -i        ignore interrupt signals
  --help    show this help`,
	"test": `Usage: test EXPRESSION
Evaluate EXPRESSION and return success when it is true.

Operators:
  -e/-f/-d/-L FILE       exists/regular/directory/symbolic link
  -r/-w/-x/-s FILE       readable/writable/executable/nonempty
  -n STRING, -z STRING   nonempty/empty string
  STRING = STRING        equal (also ==); !=, <, and > are supported
  INT -eq INT            numeric comparison (-ne, -lt, -le, -gt, -ge)
  FILE -nt/-ot/-ef FILE  newer/older/same file
  ! EXPR, EXPR -a EXPR, EXPR -o EXPR, ( EXPR )
  --help                  show this help`,
	"true": `Usage: true
Return a successful status.

Options:
  --help    show this help`,
	"cat": `Usage: cat [OPTION]... [FILE]...
Concatenate FILEs to standard output. FILE '-' means standard input.

Options:
  -n        number all output lines
  -b        number nonempty lines (overrides -n)
  -E        display '$' at newline boundaries
  -s        squeeze repeated empty lines
  -T        display TAB characters as ^I
  -A        equivalent to -ET
  --help    show this help`,
	"echo": `Usage: echo [-neE] [ARG]...
Write ARGs separated by spaces.

Options:
  -n        do not output the trailing newline
  -e        enable backslash escapes
  -E        disable backslash escapes
  --help    show this help`,
	"grep": `Usage: grep [OPTION]... PATTERN [FILE]...
Print lines that match PATTERN.

Options:
  -i        ignore case
  -v        select nonmatching lines
  -n        print line numbers
  -c        print match counts
  -l        print names of matching files
  -h/-H     suppress/force filename prefixes
  -w/-x     match whole words/whole lines
  -F/-E     fixed strings/extended regular expressions
  -r/-R     recurse through directories
  -q        stop after the first match
  -e PAT    add a pattern
  -m NUM    stop after NUM matches per file
  --help    show this help`,
	"head": `Usage: head [OPTION]... [FILE]...
Print the first part of each FILE.

Options:
  -n NUM    print the first NUM lines (default 10)
  -c NUM    print the first NUM bytes
  -q/-v     suppress/force headers
  --help    show this help`,
	"tail": `Usage: tail [OPTION]... [FILE]
Print the last part of FILE.

Options:
  -n NUM    print the last NUM lines; +NUM starts at line NUM
  -c NUM    print the last NUM bytes; +NUM starts at byte NUM
  -f        follow one file by descriptor
  -q/-v     suppress/force headers
  --help    show this help`,
	"ls": `Usage: ls [OPTION]... [FILE]...
List directory contents or FILE metadata.

Options:
  -a/-A     include hidden entries/include hidden entries except . and ..
  -l/-1     long format/one entry per line
  -h        human-readable sizes with -l
  -r        reverse sorting
  -t/-S     sort by modification time/size
  -R        recurse through subdirectories
  -d        list directories themselves
  -F        append file-type indicators
  --help    show this help`,
	"cp": `Usage: cp [OPTION]... SOURCE... DEST
Copy files or directories.

Options:
  -r/-R     copy directories recursively
  -a        recursive copy preserving modes and timestamps
  -f        remove an unopenable destination and retry
  -i        prompt before overwriting
  -p        preserve modes and timestamps
  -v        explain what is copied
  --help    show this help`,
	"mv": `Usage: mv [OPTION]... SOURCE... DEST
Move or rename files and directories.

Options:
  -f        replace destinations when possible
  -i        prompt before overwriting
  -n        do not overwrite existing destinations
  -v        explain what is moved
  --help    show this help`,
	"rm": `Usage: rm [OPTION]... FILE...
Remove files or directories.

Options:
  -r/-R     remove directories recursively
  -f        ignore missing operands and files
  -i        prompt before each top-level removal
  -d        remove empty directories
  -v        explain removals
  --no-preserve-root  allow recursive removal of '/'
  --help    show this help`,
	"mkdir": `Usage: mkdir [OPTION]... DIRECTORY...
Create directories.

Options:
  -p        create missing parents; ignore existing directories
  -m MODE   set the new final directory's octal mode
  --help    show this help`,
	"rmdir": `Usage: rmdir [OPTION]... DIRECTORY...
Remove empty directories.

Options:
  -p        remove empty parent directories too
  --ignore-fail-on-non-empty
            ignore failures caused by nonempty directories
  --help    show this help`,
	"touch": `Usage: touch [OPTION]... FILE...
Create missing FILEs and update timestamps.

Options:
  -c        do not create files
  -a        change only access time
  -m        change only modification time
  --help    show this help`,
	"pwd": `Usage: pwd [-LP]
Print the current working directory.

Options:
  -L        use the logical PWD value (default)
  -P        print the physical directory
  --help    show this help`,
	"wc": `Usage: wc [OPTION]... [FILE]...
Print newline, word, byte, and character counts.

Options:
  -l        print newline counts
  -w        print word counts
  -c        print byte counts
  -m        print character counts
  --help    show this help`,
	"sort": `Usage: sort [OPTION]... [FILE]...
Sort lines of text.

Options:
  -n        compare numeric prefixes
  -r        reverse the result
  -u        emit one line per equal key
  -f        fold case
  -b        ignore leading blanks
  -c        check ordering without producing output
  --help    show this help`,
	"uniq": `Usage: uniq [OPTION]... [INPUT [OUTPUT]]
Collapse adjacent equal lines.

Options:
  -c        prefix lines with occurrence counts
  -d        print only repeated lines
  -u        print only unique lines
  -i        ignore case
  --help    show this help`,
	"cut": `Usage: cut OPTION... [FILE]...
Select fields or character positions from each line.

Options:
  -f LIST   select fields
  -c LIST   select character positions
  -d CHAR   use CHAR as the field delimiter
  -s        suppress lines without delimiters
  --output-delimiter=STRING
            join selected fields with STRING
  --help    show this help`,
	"tr": `Usage: tr [OPTION]... SET1 [SET2]
Translate, delete, or squeeze bytes from standard input.

Options:
  -d        delete bytes in SET1
  -s        squeeze repeated bytes in the last SET
  -c/-C     complement SET1
  --help    show this help`,
	"ip": `Usage: ip OBJECT COMMAND [ARG]...
Show or change Linux addresses and routes using rtnetlink.

Objects and commands:
  ip link [show] [dev IFACE]
  ip link add NAME type bond [mode MODE] [miimon MS]
  ip link add link PARENT name NAME type vlan id VLAN_ID
  ip link set dev IFACE up|down
  ip link set dev IFACE master BOND
  ip link set dev IFACE nomaster
  ip link delete NAME
  ip addr [show] [dev IFACE]
  ip addr add ADDRESS dev IFACE
  ip addr del ADDRESS dev IFACE
  ip route [show]
  ip route add PREFIX [via GATEWAY] [dev IFACE] [metric NUM]
  ip route del PREFIX [via GATEWAY] [dev IFACE] [metric NUM]
  --help    show this help`,
	"help": `Usage: ba6 help [COMMAND]
Show general help or detailed help for COMMAND.

Options:
  --help    show this help`,
}

func helpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "--help" {
			return true
		}
	}
	return false
}

func writeAppletHelp(w io.Writer, name string) error {
	help, ok := appletHelp[name]
	if !ok {
		return fmt.Errorf("unknown applet %q", name)
	}
	_, err := fmt.Fprintln(w, help)
	return err
}

func writeGeneralHelp(w io.Writer) error {
	for _, line := range []string{
		"Usage: ba6 <applet> [args...]",
		"       ba6 help <applet>",
		"       (or symlink ba6 to an applet name)",
		"\nApplets:",
	} {
		if _, err := fmt.Fprintln(w, line); err != nil {
			return err
		}
	}
	names := make([]string, 0, len(appletHelp))
	for name := range appletHelp {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := fmt.Fprintf(w, "  %s\n", name); err != nil {
			return err
		}
	}
	return nil
}

func cmdHelp(args []string) int {
	if len(args) == 0 {
		if err := writeGeneralHelp(os.Stdout); err != nil {
			fatalf("help", "write error: %v", err)
			return 1
		}
		return 0
	}
	if len(args) > 1 {
		fatalf("help", "extra operand %q", args[1])
		return 1
	}
	if _, ok := appletHelp[args[0]]; !ok {
		fatalf("help", "unknown applet %q", args[0])
		return 1
	}
	if err := writeAppletHelp(os.Stdout, args[0]); err != nil {
		fatalf("help", "%v", err)
		return 1
	}
	return 0
}
