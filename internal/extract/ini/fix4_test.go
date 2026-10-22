package ini

import (
	"testing"

	"github.com/f-klement/errata/internal/extract/core"
	"github.com/f-klement/errata/internal/extract/coretest"
)

// A read that spends its budget is refused for its work, whichever loop
// spends the last unit, and a read with budget enough reads what an
// unmetered read reads. Each input is read under every budget from 0 to
// its cost. The planted break is php's syntax refusal ahead of the work
// refusal, for a budget that runs out inside a value.
func TestWorkMeterRefusesAtEveryBudget(t *testing.T) {
	for _, c := range []struct {
		body string
		spec Spec
	}{
		{setupCfg, ini("setup.cfg", "options", "install_requires")},
		{"[testenv]\ndeps = pytest # pin\n  coverage\ncommands = pytest \\\n  -q {posargs}\n", ini("tox.ini", "testenv", "commands")},
		{"[PHP]\na = x \"y ; z\" 'w' ; c\nb = E_ALL & ~E_DEPRECATED\nc = ${HOME:-x}\nd[] = on\ne = 1 2\nc = 128M\n", ini("php.ini", "PHP", "c")},
		{gitconfig, gitSpec(".gitconfig", "remote", ptr("origin"), "fetch")},
		{inventory, Spec{File: "inventory/hosts", Section: "web", Host: "web3.example.com", Key: "x"}},
	} {
		e, err := Build(c.spec)
		if err != nil {
			t.Fatal(err)
		}
		coretest.RefusesAtEveryBudget(t, e.(core.EntryExtractor), c.body)
	}
}
