package workspace_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestExternalWorkspaceTestsDeclareParallelIsolation(t *testing.T) {
	t.Parallel()

	_, contractPath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test isolation contract source")
	}
	fileSet := token.NewFileSet()
	packages, err := parser.ParseDir(
		fileSet,
		filepath.Dir(contractPath),
		func(info os.FileInfo) bool {
			return strings.HasSuffix(info.Name(), "_test.go")
		},
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	external, ok := packages["workspace_test"]
	if !ok {
		t.Fatal("external workspace test package was not parsed")
	}

	// Exceptions are deliberately narrow and must explain why sharing process
	// state cannot affect another parallel test. Keep this empty by default.
	exceptions := map[string]string{
		"integration_template_test.go:<package>:mutable package global \"targetRepositoryTemplateSHA1\"":            "sync.OnceValues publishes one immutable template; every caller clones it before mutation.",
		"integration_template_test.go:<package>:mutable package global \"targetRepositoryTemplateSHA256\"":          "sync.OnceValues publishes one immutable template; every caller clones it before mutation.",
		"integration_template_test.go:<package>:mutable package global \"realIntegrationRepositoryTemplateSHA1\"":   "sync.OnceValues publishes one immutable template; every caller clones it before mutation.",
		"integration_template_test.go:<package>:mutable package global \"realIntegrationRepositoryTemplateSHA256\"": "sync.OnceValues publishes one immutable template; every caller clones it before mutation.",
	}
	usedExceptions := make(map[string]bool, len(exceptions))
	violations := make([]string, 0)
	addViolation := func(filename, scope, rule string, position token.Pos) {
		filename = filepath.Base(filename)
		key := filename + ":" + scope + ":" + rule
		if reason, allowed := exceptions[key]; allowed {
			usedExceptions[key] = true
			if strings.TrimSpace(reason) == "" {
				violations = append(violations, "empty isolation exception reason for "+key)
			}
			return
		}
		line := strconv.Itoa(fileSet.Position(position).Line)
		violations = append(violations, filename+":"+line+": "+rule+" in "+scope)
	}

	packageGlobals := make(map[string]struct{})
	for filename, file := range external.Files {
		for _, declaration := range file.Decls {
			general, ok := declaration.(*ast.GenDecl)
			if !ok || general.Tok != token.VAR {
				continue
			}
			for _, specification := range general.Specs {
				value, ok := specification.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for _, name := range value.Names {
					if name.Name == "_" {
						continue
					}
					packageGlobals[name.Name] = struct{}{}
					addViolation(
						filename,
						"<package>",
						"mutable package global "+strconv.Quote(name.Name),
						name.Pos(),
					)
				}
			}
		}
	}

	for filename, file := range external.Files {
		imports := contractImports(file)
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			scope := function.Name.Name
			if strings.HasPrefix(scope, "Test") {
				parameter := contractTestParameter(function)
				if parameter == "" || !contractCallsParallel(function.Body, parameter) {
					addViolation(filename, scope, "missing t.Parallel()", function.Pos())
				}
			}
			ast.Inspect(function.Body, func(node ast.Node) bool {
				switch node := node.(type) {
				case *ast.CallExpr:
					if rule := contractProcessMutation(node, imports); rule != "" {
						addViolation(filename, scope, rule, node.Pos())
					}
				case *ast.AssignStmt:
					for _, target := range node.Lhs {
						if name := contractAssignedRoot(target); name != "" {
							if _, global := packageGlobals[name]; global {
								addViolation(
									filename, scope,
									"assignment to package global "+strconv.Quote(name),
									target.Pos(),
								)
							}
						}
					}
				case *ast.IncDecStmt:
					if name := contractAssignedRoot(node.X); name != "" {
						if _, global := packageGlobals[name]; global {
							addViolation(
								filename, scope,
								"assignment to package global "+strconv.Quote(name),
								node.Pos(),
							)
						}
					}
				}
				return true
			})
		}
	}

	for key := range exceptions {
		if !usedExceptions[key] {
			violations = append(violations, "unused isolation exception "+key)
		}
	}
	if len(violations) == 0 {
		return
	}
	sort.Strings(violations)
	t.Fatalf("external workspace test isolation contract failed:\n%s", strings.Join(violations, "\n"))
}

func contractImports(file *ast.File) map[string]string {
	imports := make(map[string]string, len(file.Imports))
	for _, specification := range file.Imports {
		path, err := strconv.Unquote(specification.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if specification.Name != nil {
			name = specification.Name.Name
		}
		imports[name] = path
	}
	return imports
}

func contractTestParameter(function *ast.FuncDecl) string {
	if function.Type.Params == nil || len(function.Type.Params.List) != 1 ||
		len(function.Type.Params.List[0].Names) != 1 {
		return ""
	}
	return function.Type.Params.List[0].Names[0].Name
}

func contractCallsParallel(body *ast.BlockStmt, parameter string) bool {
	found := false
	ast.Inspect(body, func(node ast.Node) bool {
		if found {
			return false
		}
		if _, nested := node.(*ast.FuncLit); nested {
			return false
		}
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != "Parallel" || len(call.Args) != 0 {
			return true
		}
		receiver, ok := selector.X.(*ast.Ident)
		found = ok && receiver.Name == parameter
		return !found
	})
	return found
}

func contractProcessMutation(call *ast.CallExpr, imports map[string]string) string {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	packagePath := ""
	if identifier, ok := selector.X.(*ast.Ident); ok {
		packagePath = imports[identifier.Name]
	}
	switch {
	case packagePath == "os" && selector.Sel.Name == "Setenv":
		return "process-global call os.Setenv"
	case packagePath == "os" && selector.Sel.Name == "Unsetenv":
		return "process-global call os.Unsetenv"
	case packagePath == "os" && selector.Sel.Name == "Clearenv":
		return "process-global call os.Clearenv"
	case packagePath == "os" && selector.Sel.Name == "Chdir":
		return "process-global call os.Chdir"
	case packagePath == "syscall" && selector.Sel.Name == "Umask":
		return "process-global call syscall.Umask"
	case selector.Sel.Name == "Setenv":
		return "process-global call testing.T.Setenv"
	case selector.Sel.Name == "Chdir":
		return "process-global call testing.T.Chdir"
	default:
		return ""
	}
}

func contractAssignedRoot(expression ast.Expr) string {
	switch expression := expression.(type) {
	case *ast.Ident:
		return expression.Name
	case *ast.SelectorExpr:
		return contractAssignedRoot(expression.X)
	case *ast.IndexExpr:
		return contractAssignedRoot(expression.X)
	case *ast.IndexListExpr:
		return contractAssignedRoot(expression.X)
	case *ast.ParenExpr:
		return contractAssignedRoot(expression.X)
	case *ast.StarExpr:
		return contractAssignedRoot(expression.X)
	default:
		return ""
	}
}
