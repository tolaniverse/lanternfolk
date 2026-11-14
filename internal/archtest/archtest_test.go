// Package archtest fails the build when the dependency rule from the LLD (§2)
// is broken:
//
//	protocol        → stdlib only
//	internal/core   → stdlib and internal/core only
//	internal/client → never internal/server, internal/core, internal/adapters
//	internal/server → never internal/adapters (it declares ports; cmd wires them)
//	UI toolkits     → only from cmd/* and internal/ui/*
package archtest

import (
	"bufio"
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

const module = "github.com/tolaniverse/lanternfolk"

var uiToolkits = []string{"github.com/egoist/asuka", "github.com/tolaniverse/opentui-go"}

type rule struct {
	name   string
	from   func(pkg string) bool
	banned func(imp string) bool
}

func under(prefix string) func(string) bool {
	p := module + "/" + prefix
	return func(s string) bool { return s == p || strings.HasPrefix(s, p+"/") }
}

func isStdlib(imp string) bool {
	first, _, _ := strings.Cut(imp, "/")
	return !strings.Contains(first, ".")
}

func isUIToolkit(imp string) bool {
	for _, t := range uiToolkits {
		if imp == t || strings.HasPrefix(imp, t+"/") {
			return true
		}
	}
	return false
}

var rules = []rule{
	{"protocol imports only the standard library", under("protocol"),
		func(imp string) bool { return !isStdlib(imp) }},
	{"internal/core imports only the standard library and itself", under("internal/core"),
		func(imp string) bool { return !isStdlib(imp) && !under("internal/core")(imp) }},
	{"internal/client knows only the wire protocol", under("internal/client"),
		func(imp string) bool {
			return under("internal/server")(imp) || under("internal/core")(imp) || under("internal/adapters")(imp)
		}},
	{"internal/server depends on ports, not adapters", under("internal/server"), under("internal/adapters")},
	{"UI toolkits are imported only by cmd/* and internal/ui/*",
		func(pkg string) bool { return !under("cmd")(pkg) && !under("internal/ui")(pkg) },
		isUIToolkit},
}

// imports lists every package in the module with its direct imports
// (test files included, since test code must follow the rule too).
func imports(t *testing.T) map[string][]string {
	t.Helper()
	cmd := exec.Command("go", "list", "-f",
		`{{.ImportPath}} {{join .Imports " "}} {{join .TestImports " "}} {{join .XTestImports " "}}`,
		module+"/...")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}
	pkgs := map[string][]string{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) > 0 {
			pkgs[fields[0]] = fields[1:]
		}
	}
	if len(pkgs) == 0 {
		t.Fatal("go list returned no packages")
	}
	return pkgs
}

func TestDependencyRule(t *testing.T) {
	for pkg, imps := range imports(t) {
		for _, r := range rules {
			if !r.from(pkg) {
				continue
			}
			for _, imp := range imps {
				if r.banned(imp) {
					t.Errorf("%s imports %s: %s", pkg, imp, r.name)
				}
			}
		}
	}
}

func TestRulesCatchViolations(t *testing.T) {
	cases := []struct{ pkg, imp string }{
		{module + "/protocol", "github.com/some/dep"},
		{module + "/internal/core/chat", module + "/internal/server/app"},
		{module + "/internal/client/core", module + "/internal/core/ids"},
		{module + "/internal/server/app", module + "/internal/adapters/postgres"},
		{module + "/internal/server/gateway", "github.com/egoist/asuka/ui"},
	}
	for _, c := range cases {
		caught := false
		for _, r := range rules {
			if r.from(c.pkg) && r.banned(c.imp) {
				caught = true
			}
		}
		if !caught {
			t.Errorf("no rule catches %s → %s", c.pkg, c.imp)
		}
	}
}
