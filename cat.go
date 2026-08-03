package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
)

// cmdCat implements a subset of cat(1): -n (number all lines), -b (number
// nonblank lines, overrides -n), -E (show $ at line ends), -s (squeeze repeated
// blank lines), and -T (show tabs as ^I). With no files, or "-", it reads stdin.
func cmdCat(args []string) int {
	var (
		numberAll  bool
		numberNon  bool
		showEnds   bool
		squeeze    bool
		showTabs   bool
		files      []string
		noMoreOpts bool
	)

	for _, a := range args {
		if !noMoreOpts && a == "--" {
			noMoreOpts = true
			continue
		}
		if !noMoreOpts && len(a) > 1 && a[0] == '-' {
			for _, c := range a[1:] {
				switch c {
				case 'n':
					numberAll = true
				case 'b':
					numberNon = true
				case 'E':
					showEnds = true
				case 's':
					squeeze = true
				case 'T':
					showTabs = true
				case 'A':
					showEnds, showTabs = true, true
				default:
					fatalf("cat", "invalid option -- '%c'", c)
					return 1
				}
			}
			continue
		}
		files = append(files, a)
	}
	if len(files) == 0 {
		files = []string{"-"}
	}

	plain := !numberAll && !numberNon && !showEnds && !squeeze && !showTabs

	out := bufio.NewWriter(os.Stdout)

	lineNo := 1
	prevBlank := false
	status := 0

	for _, fname := range files {
		f, err := openInput(fname)
		if err != nil {
			fatalf("cat", "%s: %v", fname, err)
			status = 1
			continue
		}

		// Fast path: plain copy with no transformations.
		if plain {
			if _, copyErr := io.Copy(out, f); copyErr != nil {
				fatalf("cat", "%s: %v", fname, copyErr)
				status = 1
			}
			if closeErr := f.Close(); closeErr != nil {
				fatalf("cat", "%s: %v", fname, closeErr)
				status = 1
			}
			continue
		}

		r := bufio.NewReader(f)
		for {
			line, err := r.ReadString('\n')
			if len(line) == 0 && err != nil {
				if !errors.Is(err, io.EOF) {
					fatalf("cat", "%s: %v", fname, err)
					status = 1
				}
				break
			}

			body := line
			hadNL := false
			if len(body) > 0 && body[len(body)-1] == '\n' {
				body = body[:len(body)-1]
				hadNL = true
			}
			isBlank := len(body) == 0

			if squeeze && isBlank && prevBlank {
				if err != nil {
					break
				}
				continue
			}
			prevBlank = isBlank

			if numberNon {
				if !isBlank {
					fmt.Fprintf(out, "%6d\t", lineNo)
					lineNo++
				}
			} else if numberAll {
				fmt.Fprintf(out, "%6d\t", lineNo)
				lineNo++
			}

			if showTabs {
				for i := 0; i < len(body); i++ {
					if body[i] == '\t' {
						_, _ = out.WriteString("^I") // Flush reports the writer's sticky error.
					} else {
						_ = out.WriteByte(body[i]) // Flush reports the writer's sticky error.
					}
				}
			} else {
				_, _ = out.WriteString(body) // Flush reports the writer's sticky error.
			}

			if showEnds && hadNL {
				_ = out.WriteByte('$') // Flush reports the writer's sticky error.
			}
			if hadNL {
				_ = out.WriteByte('\n') // Flush reports the writer's sticky error.
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					fatalf("cat", "%s: %v", fname, err)
					status = 1
				}
				break
			}
		}
		if closeErr := f.Close(); closeErr != nil {
			fatalf("cat", "%s: %v", fname, closeErr)
			status = 1
		}
	}
	if err := out.Flush(); err != nil {
		fatalf("cat", "write error: %v", err)
		status = 1
	}
	return status
}
