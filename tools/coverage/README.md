# Coverage tooling

Scripts that regenerate the measurements behind [`COVERAGE.md`](../../COVERAGE.md)
and the verbatim-text check behind [`PROVENANCE.md`](../../PROVENANCE.md).

They are Python, deliberately outside the Go module: nothing here ships in the
binary and the build stays dependency-free.

## Safety rule

**Never execute a tool to discover its options.** These scripts read `man` pages,
which never run the program.

This is not hypothetical. An earlier version of this tooling looped over ba6's own
applet list running `<name> --help` and then `<name> -h`. That list contains
`halt`, `poweroff`, `reboot` and `init`; on systemd `/usr/bin/poweroff` is a
symlink to `systemctl` where **`-h` is an undocumented compatibility no-op, not
`--help`**, and polkit lets an active local session power off without a password.
The machine shut down three times before the cause was found.

`behaviour_diff.sh` does run applets, but only those named in its explicit
`ALLOWLIST`, and only in a throwaway directory. Extend that list by hand, never
from `main.go`.

## Usage

```sh
python3 ref_options.py        > ref.json     # option groups from man pages
python3 ba6_options.py        > ba6.json     # option sets from the Go source
python3 compare.py ref.json ba6.json         # coverage table + missing options
python3 text_overlap.py                      # verbatim help-text check, must print 0
sh      behaviour_diff.sh                    # side-by-side output diffs
```

`ba6_options.py` extracts a *superset*: it collects option runes from `switch`
statements, some of which are not options at all (escape sequences in `tr`, format
directives in `date` and `stat`, command letters in `sed`). Cross-check anything
surprising against `behaviour_diff.sh` before believing it.
