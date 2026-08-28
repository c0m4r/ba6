// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 c0m4r

package main

import (
	"strings"
	"testing"
)

func TestAwkControlFlowAndArrays(t *testing.T) {
	cases := []struct {
		name   string
		script string
		input  string
		want   string
	}{
		{"for loop", "BEGIN{for(i=0;i<3;i++) print i}", "", "0\n1\n2\n"},
		{"while loop", "BEGIN{i=0; while(i<3){print i; i++}}", "", "0\n1\n2\n"},
		{"do-while runs at least once", "BEGIN{i=5; do {print i; i++} while(i<3)}", "", "5\n"},
		{"break", "BEGIN{for(i=0;i<10;i++){if(i==3)break; print i}}", "", "0\n1\n2\n"},
		{"continue", "BEGIN{for(i=0;i<5;i++){if(i==2)continue; print i}}", "", "0\n1\n3\n4\n"},
		{"array basic", `BEGIN{a["x"]=1; a["y"]=2; print a["x"], a["y"]}`, "", "1 2\n"},
		{"in operator", `BEGIN{a["x"]=1; print ("x" in a), ("y" in a)}`, "", "1 0\n"},
		{"delete", `BEGIN{a["x"]=1; delete a["x"]; print ("x" in a)}`, "", "0\n"},
		{"for-in count", `BEGIN{a["x"]=1;a["y"]=2;a["z"]=3;n=0;for(k in a)n++;print n}`, "", "3\n"},
		{"multi-dim array", `BEGIN{a[1,2]="x"; print a[1,2]; print length(a)}`, "", "x\n1\n"},
		{"decrement", "BEGIN{i=5; i--; print i}", "", "4\n"},
		{"prefix increment", "BEGIN{i=5; ++i; print i}", "", "6\n"},
		{"field assignment in loop", `{for(i=1;i<=NF;i++) $i=$i"!"; print}`, "a b c\n", "a! b! c!\n"},
		{"index builtin", `BEGIN{print index("hello world", "world")}`, "", "7\n"},
		{"index builtin no match", `BEGIN{print index("hello", "z")}`, "", "0\n"},
		{"split builtin", `BEGIN{n=split("a:b:c", arr, ":"); print n, arr[1], arr[2], arr[3]}`, "", "3 a b c\n"},
		{"match builtin", `BEGIN{print match("hello world", /wor/), RSTART, RLENGTH}`, "", "7 7 3\n"},
		{"match builtin no match", `BEGIN{print match("hello", /z/), RSTART, RLENGTH}`, "", "0 0 -1\n"},
		{"sprintf builtin", `BEGIN{print sprintf("%05d-%s", 7, "x")}`, "", "00007-x\n"},
		{"range pattern", "/start/,/end/", "a\nstart\nb\nend\nc\n", "start\nb\nend\n"},
		{"simple concat", `BEGIN{print "x=" 5}`, "", "x=5\n"},
		{"concat two vars", `BEGIN{a="foo";b="bar";print a b}`, "", "foobar\n"},
		{"subtraction is not concat", "BEGIN{a=5;b=3;print a - b}", "", "2\n"},
		{"length of record", "{print length}", "hello\n", "5\n"},
		{"length of string", `BEGIN{print length("hello")}`, "", "5\n"},
		{"fizzbuzz", `BEGIN{for(i=1;i<=5;i++){if(i%3==0)print "Fizz";else if(i%5==0)print "Buzz";else print i}}`, "", "1\n2\nFizz\n4\nBuzz\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, out, stderr := captureApplet(t, cmdAwk, []string{tc.script}, tc.input)
			if status != 0 || out != tc.want {
				t.Fatalf("awk %q on %q = (%d, %q, %q), want %q", tc.script, tc.input, status, out, stderr, tc.want)
			}
		})
	}
}

func TestAwkWordFrequencyProgram(t *testing.T) {
	// A realistic multi-line program combining loops, fields, and arrays:
	// order from a Go map range is unspecified, same as awk's own for-in, so
	// this checks the total count and each pair rather than exact output.
	script := `{for(i=1;i<=NF;i++)count[$i]++} END{for(w in count) printf "%s=%d\n", w, count[w]}`
	status, out, stderr := captureApplet(t, cmdAwk, []string{script}, "the quick brown fox\nthe lazy fox\n")
	if status != 0 {
		t.Fatalf("awk word frequency = (%d, %q, %q)", status, out, stderr)
	}
	want := map[string]string{"the": "2", "quick": "1", "brown": "1", "fox": "2", "lazy": "1"}
	lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(want), out)
	}
	for _, line := range lines {
		word, count, ok := strings.Cut(line, "=")
		if !ok || want[word] != count {
			t.Fatalf("unexpected line %q in %q", line, out)
		}
	}
}

func TestAwkArrayScopingAndBreakDoesNotLeakIntoOuterLoop(t *testing.T) {
	// A break inside a nested for must only stop the inner loop.
	script := `BEGIN{
		for(i=0;i<3;i++){
			for(j=0;j<10;j++){
				if(j==2) break
			}
			total += j
		}
		print total
	}`
	status, out, _ := captureApplet(t, cmdAwk, []string{script}, "")
	if status != 0 || out != "6\n" {
		t.Fatalf("nested break = (%d, %q), want \"6\\n\" (j==2 three times)", status, out)
	}
}
