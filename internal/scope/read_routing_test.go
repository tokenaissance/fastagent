package scope

// This file is the enforcement half of the read-model convergence: the scope
// package is where the decision "does this settings/provider read come from the
// configs blob or from configs_kv" lives, and that only stays true if no adapter
// reaches around it. A comment saying so would rot; this walks the source tree
// and fails when one does.
//
// The rule it encodes:
//
//   - The blob-row read methods (GetConfigByName, ListConfigs, ListConfigsByUser,
//     BatchGetConfigsByAgentIDs) may only be called with a *mirrored* kind
//     (setting / provider / plugin_enabled) from inside internal/scope or
//     internal/store. Everyone else asks for a read model — SettingAt,
//     ExactSetting, ProvidersAt, SettingNamesAt, AgentScopeRows, RowsAt — and
//     lets this package pick the table.
//   - kind="channel" is exempt in the other direction: channels have no
//     configs_kv half at all (they have their own channels table), so reading
//     them from the blob is not a migration hazard. Those callers go through
//     scope.RowsAt anyway, so the exemption is about the kind, not the method.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// blobRowReads are the store methods that read a configs row. A caller outside
// the layer reaching for one of these with a mirrored kind is the regression
// this test exists to catch; the shipped code has none.
var blobRowReads = map[string]bool{
	"GetConfigByName":           true,
	"ListConfigs":               true,
	"ListConfigsByUser":         true,
	"BatchGetConfigsByAgentIDs": true,
}

// mirroredKinds are the kinds that have a configs_kv mirror, so a read of
// them is a read the layer must route.
var mirroredKinds = map[string]bool{
	"KindSetting":       true,
	"KindProvider":      true,
	"KindPluginEnabled": true,
}

// layerOwnedPackages may name the blob methods directly. store implements them;
// scope is the layer. Nothing else.
var layerOwnedPackages = map[string]bool{
	"internal/store": true,
	"internal/scope": true,
}

func TestConfigsKVReadsGoThroughTheScopeLayer(t *testing.T) {
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	var offenders []string

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata", "bin", "previews":
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if layerOwnedPackages[filepath.ToSlash(filepath.Dir(rel))] {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			method, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || !blobRowReads[method.Sel.Name] {
				return true
			}
			for _, arg := range call.Args {
				kind, ok := kindLiteral(arg)
				if ok && mirroredKinds[kind] {
					offenders = append(offenders, fmt.Sprintf(
						"%s:%d: %s(…, store.%s, …) — a %s read must go through the scope read model (SettingAt / ExactSetting / ProvidersAt / SettingNamesAt / AgentScopeRows / RowsAt)",
						rel, fset.Position(call.Pos()).Line, method.Sel.Name, kind, strings.TrimPrefix(kind, "Kind")))
					return true
				}
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		sort.Strings(offenders)
		t.Fatalf("blob row reads outside the configs layer:\n  %s", strings.Join(offenders, "\n  "))
	}
}

// kindLiteral resolves the argument forms the kind is written in: the
// qualified constant (store.KindSetting) and the bare one (KindSetting, for a
// file inside the store package — which the walk already skips, but the helper
// being total keeps the check honest if the skip list ever changes).
func kindLiteral(arg ast.Expr) (string, bool) {
	switch v := arg.(type) {
	case *ast.SelectorExpr:
		return v.Sel.Name, true
	case *ast.Ident:
		return v.Name, true
	}
	return "", false
}

// TestBlobRowReadsAreStillStoreMethods keeps the guard above from silently
// lapsing: a renamed or removed store method would make a call site unmatchable
// and the walk would pass with the violation still present.
func TestBlobRowReadsAreStillStoreMethods(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "store", "store.go"))
	if err != nil {
		t.Fatalf("read store.go: %v", err)
	}
	for name := range blobRowReads {
		if !strings.Contains(string(src), name+"(") {
			t.Errorf("store.Store has no %s — update blobRowReads in this test", name)
		}
	}
}
