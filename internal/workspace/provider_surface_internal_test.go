package workspace

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestProviderV2SurfaceHasOnlyTypedWritesAndNoCommandAssembly(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate provider source")
	}
	directory := filepath.Dir(currentFile)
	paths, err := filepath.Glob(filepath.Join(directory, "provider*.go"))
	if err != nil {
		t.Fatal(err)
	}
	files := make([]*ast.File, 0, len(paths))
	set := token.NewFileSet()
	for _, path := range paths {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(set, path, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, file)
		for _, imported := range file.Imports {
			value, _ := strconv.Unquote(imported.Path.Value)
			if value == "os/exec" {
				t.Fatalf("provider v2 source imports executable command runner in %s", filepath.Base(path))
			}
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.CallExpr:
				if selector, ok := value.Fun.(*ast.SelectorExpr); ok &&
					(selector.Sel.Name == "Command" || selector.Sel.Name == "CommandContext") {
					t.Errorf("provider v2 source assembles executable command at %s", set.Position(value.Pos()))
				}
			case *ast.BasicLit:
				if value.Kind != token.STRING {
					break
				}
				literal, _ := strconv.Unquote(value.Value)
				lower := strings.ToLower(literal)
				for _, forbidden := range []string{
					"git push", "gh pr", "remote_delete", "delete_remote", "close_pull_request", "no_merge",
				} {
					if strings.Contains(lower, forbidden) {
						t.Errorf("provider v2 source contains forbidden command/action literal %q at %s", literal, set.Position(value.Pos()))
					}
				}
			}
			return true
		})
	}

	intentKinds := providerTypedStringConstants(files, "ProviderIntentKind")
	wantKinds := []string{"merge", "open_pull_request", "push"}
	if strings.Join(intentKinds, "\x00") != strings.Join(wantKinds, "\x00") {
		t.Fatalf("provider intent kinds = %#v, want %#v", intentKinds, wantKinds)
	}
	strategies := providerTypedStringConstants(files, "ProviderMergeStrategy")
	if len(strategies) != 1 || strategies[0] != "merge_commit" {
		t.Fatalf("provider merge strategies = %#v, want merge_commit only", strategies)
	}

	var adapterMethods []string
	for _, file := range files {
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.TYPE {
				continue
			}
			for _, specification := range general.Specs {
				typeSpec := specification.(*ast.TypeSpec)
				if typeSpec.Name.Name != "providerAdapterPort" {
					continue
				}
				iface := typeSpec.Type.(*ast.InterfaceType)
				for _, method := range iface.Methods.List {
					for _, name := range method.Names {
						adapterMethods = append(adapterMethods, name.Name)
					}
				}
			}
		}
	}
	sort.Strings(adapterMethods)
	wantMethods := []string{"Merge", "OpenPullRequest", "Push", "QueryIntent", "QueryPullRequest"}
	if strings.Join(adapterMethods, "\x00") != strings.Join(wantMethods, "\x00") {
		t.Fatalf("provider adapter methods = %#v, want %#v", adapterMethods, wantMethods)
	}
}

func providerTypedStringConstants(files []*ast.File, typeName string) []string {
	var values []string
	for _, file := range files {
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.CONST {
				continue
			}
			for _, specification := range general.Specs {
				value := specification.(*ast.ValueSpec)
				identifier, ok := value.Type.(*ast.Ident)
				if !ok || identifier.Name != typeName || len(value.Values) != 1 {
					continue
				}
				literal, ok := value.Values[0].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					continue
				}
				decoded, _ := strconv.Unquote(literal.Value)
				values = append(values, decoded)
			}
		}
	}
	sort.Strings(values)
	return values
}
