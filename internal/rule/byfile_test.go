package rule

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"cel.dev/cel-go/common/types"

	"github.com/f-klement/errata/internal/check"
)

// by_file(x) groups x's values by the file each came from, so a rule can
// say that every file that sets one value sets another: here every
// Application that ignores a Secret's data syncs with
// RespectIgnoreDifferences. The rule holds when each such file has the
// option, and is false, with every value and its file in the message, when
// one lacks it. Planted breaks: the values grouped under one key, which
// passed the file without the option; the files of the values not handed
// to Eval, which made the rule unknown.
func TestByFileGroupsTheValuesOfEachFile(t *testing.T) {
	expr := "by_file(ignores).all(f, f in by_file(options) && 'RespectIgnoreDifferences=true' in by_file(options)[f])"
	ignores := named("ignores", fake{values: []check.Value{at("Secret", "apps/a.yaml", 9), at("Secret", "apps/b.yaml", 12)}})
	holds := named("options", fake{values: []check.Value{
		at("ServerSideApply=true", "apps/a.yaml", 20), at("RespectIgnoreDifferences=true", "apps/a.yaml", 21),
		at("RespectIgnoreDifferences=true", "apps/b.yaml", 30), at("CreateNamespace=true", "apps/c.yaml", 4),
	}})
	if res := Run(context.Background(), ".", assertRule(t, expr, ignores, holds)); res.Verdict != check.VerdictTrue {
		t.Fatalf("every file has the option: %v %s", res.Verdict, res.Message)
	}
	lacks := named("options", fake{values: []check.Value{
		at("RespectIgnoreDifferences=true", "apps/a.yaml", 21), at("ServerSideApply=true", "apps/b.yaml", 30),
		at("RespectIgnoreDifferences=true", "apps/c.yaml", 4),
	}})
	res := Run(context.Background(), ".", assertRule(t, expr, ignores, lacks))
	if res.Verdict != check.VerdictFalse || !strings.Contains(res.Message, `options[1] = "ServerSideApply=true" at apps/b.yaml:30`) {
		t.Errorf("b.yaml lacks the option: %v %s", res.Verdict, res.Message)
	}
}

// by_file(x) holds each file's values in the order x found them, under
// files in the order of no matter, and a file that gave no value is no key.
// Planted break: a later value of a file replacing the earlier.
func TestByFileKeepsOrderAndLeavesOutFilesWithoutValues(t *testing.T) {
	v := named("v", fake{values: []check.Value{at("1", "a", 1), at("2", "b", 1), at("3", "a", 2)}})
	for expr, want := range map[string]check.Verdict{
		"by_file(v)['a'] == ['1', '3'] && by_file(v)['b'] == ['2']": check.VerdictTrue,
		"size(by_file(v)) == 2":    check.VerdictTrue,
		"!('c' in by_file(v))":     check.VerdictTrue,
		"by_file(v)['a'] == ['3']": check.VerdictFalse,
	} {
		if res := Run(context.Background(), ".", assertRule(t, expr, v)); res.Verdict != want {
			t.Errorf("%s: %v, want %v: %s", expr, res.Verdict, want, res.Message)
		}
	}
	empty := named("e", fake{})
	c := assertRule(t, "size(by_file(e)) == 0", empty)
	c.Rule().AllowEmpty = []string{"e"}
	if res := Run(context.Background(), ".", c); res.Verdict != check.VerdictTrue {
		t.Errorf("an empty extractor's by_file: %v %s", res.Verdict, res.Message)
	}
}

// by_file takes an extractor's name and nothing else, refused at load at
// the argument; an extractor read only through by_file counts as used.
// Planted breaks: the macro taking any identifier, which loaded by_file(w)
// and failed it at run time; an extractor read through by_file reported
// as unused.
func TestByFileRefusesWhatIsNoExtractor(t *testing.T) {
	for expr, want := range map[string]string{
		"size(by_file(w)) > 0":        "assert: by_file takes the name of one of the rule's extractors, such as by_file(apps), at line 1 column 14 of the expression:\n    size(by_file(w)) > 0\n                 ^",
		"size(by_file('v')) > 0":      "by_file takes the name of one of the rule's extractors",
		"size(by_file(v[0])) > 0":     "by_file takes the name of one of the rule's extractors",
		"size(by_file(v, v)) > 0":     "undeclared reference to 'by_file'",
		"size(@errata_by_file_v) > 0": "Syntax error",
	} {
		_, err := Compile(expr, []string{"v"}, Lists{})
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", expr, err, want)
		}
	}
	if _, err := Compile("size(by_file(v)) > 0", []string{"v"}, Lists{}); err != nil {
		t.Errorf("an extractor read only through by_file: %v", err)
	}
}

// Values whose files are not given one to one are refused, so no value is
// grouped under a file that did not give it, and the rule is unknown.
// Planted break: the count left unchecked, which indexed past the files.
func TestByFileRefusesValuesWithoutTheirFiles(t *testing.T) {
	a, err := Compile("size(by_file(v)) > 0", []string{"v"}, Lists{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Eval(context.Background(), map[string][]string{"v": {"1", "2"}}, map[string][]string{"v": {"a"}}, nil)
	if err == nil || err.Error() != "by_file(v): 2 values came with 1 files, so they cannot be grouped" {
		t.Errorf("got %v", err)
	}
	if ok, err := a.Eval(context.Background(), map[string][]string{"v": {"1"}}, map[string][]string{"v": {"a"}}, nil); err != nil || !ok {
		t.Errorf("one value in one file: %v, %v", ok, err)
	}
}

// by_file's keys come in the order of each file's first value, so a
// comprehension over them, and the first error it meets, is the same on
// every run: the review's two expressions, evaluated 200 times, each give
// one outcome, the first file first, and for all the error of f0, the last
// file, since all runs every member and the rule reports one error. Planted
// break: a Go map handed to CEL, whose order
// changed the verdict 137 times in 200 and the message ten ways.
func TestByFileIteratesInTheOrderOfTheValues(t *testing.T) {
	var vals, files []string
	for i := range 10 {
		vals = append(vals, "x")
		files = append(files, fmt.Sprintf("f%d", 9-i))
	}
	for expr, want := range map[string]string{
		"by_file(v).map(f, f)[0] == 'f9'": "true <nil>",
		"by_file(v).map(f, f) == ['f9', 'f8', 'f7', 'f6', 'f5', 'f4', 'f3', 'f2', 'f1', 'f0']": "true <nil>",
		"by_file(v).all(f, by_file(w)[f].size() > 0)":                                          "false no such key: f0",
	} {
		names := []string{"v"}
		if strings.Contains(expr, "(w)") {
			names = append(names, "w")
		}
		a, err := Compile(expr, names, Lists{})
		if err != nil {
			t.Fatal(err)
		}
		for range 200 {
			ok, err := a.Eval(context.Background(), map[string][]string{"v": vals, "w": {"y"}}, map[string][]string{"v": files, "w": {"g"}}, nil)
			if got := fmt.Sprintf("%v %v", ok, err); got != want {
				t.Fatalf("%s: %s, want %s", expr, got, want)
			}
		}
	}
}

// The keys' iterator yields each key once, in order, then says it has none
// and refuses Next with an error, never a key. Planted breaks: HasNext
// true past the end; Next yielding a key twice.
func TestKeyIteratorEnds(t *testing.T) {
	it := &keyIterator{keys: []string{"a", "b"}}
	var got []string
	for range 3 {
		if it.HasNext() != types.True {
			break
		}
		got = append(got, string(it.Next().(types.String)))
	}
	if !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("keys %q, want a then b", got)
	}
	if it.HasNext() != types.False {
		t.Error("HasNext is true past the end")
	}
	if _, ok := it.Next().(*types.Err); !ok {
		t.Error("Next past the end is no error")
	}
	if it.Type() != types.IteratorType || it.Equal(types.String("a")) != types.False || it.Value() != it {
		t.Error("the iterator's type, equality or value")
	}
	if _, err := it.ConvertToNative(nil); err == nil {
		t.Error("ConvertToNative gave no error")
	}
	if _, ok := it.ConvertToType(types.StringType).(*types.Err); !ok {
		t.Error("ConvertToType gave no error")
	}
}
