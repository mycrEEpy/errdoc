package main

import (
	"fmt"
	"go/ast"
	"go/types"
	"os"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
)

// analyzer traverses function bodies to collect concrete error types.
// It recursively follows function calls into dependency packages and
// caches results to avoid redundant work. A visited set prevents
// infinite recursion on cyclic call graphs.
type analyzer struct {
	errInterface *types.Interface
	errType      types.Type
	funcIndex    map[*types.Func]funcEntry
	funcCache    map[*types.Func]map[string]bool
	visited      map[*types.Func]bool // cycle detection
}

// funcEntry pairs a function declaration's AST node with the type
// information of the package it belongs to.
type funcEntry struct {
	decl *ast.FuncDecl
	info *types.Info
}

func main() {
	if len(os.Args) < 2 {
		fmt.Printf("usage: errdoc <file.go|directory>\n")
		os.Exit(1)
	}

	target := os.Args[1]

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports,
	}

	// Determine whether target is a directory or a single file.
	var query string
	var isDir bool

	if info, err := os.Stat(target); err == nil && info.IsDir() {
		query = "./" + target
		isDir = true
	} else {
		query = "file=" + target
	}

	pkgs, err := packages.Load(cfg, query)
	if err != nil {
		fmt.Printf("load: %v\n", err)
		os.Exit(1)
	}

	if len(pkgs) == 0 {
		fmt.Printf("no packages found\n")
		os.Exit(1)
	}

	pkg := pkgs[0]
	if len(pkg.Errors) > 0 {
		for _, e := range pkg.Errors {
			fmt.Printf("%v\n", e)
		}

		os.Exit(1)
	}

	a := newAnalyzer()
	a.indexPkg(pkg)

	var files []*ast.File

	if isDir {
		files = pkg.Syntax
	} else {
		f := findFile(pkg, target)
		if f == nil {
			fmt.Printf("file %s not found in package\n", target)
			os.Exit(1)
		}

		files = []*ast.File{f}
	}

	for _, file := range files {
		a.printFileErrors(file, pkg.TypesInfo)
	}
}

// newAnalyzer creates an analyzer ready to index packages and collect errors.
func newAnalyzer() *analyzer {
	return &analyzer{
		errInterface: types.Universe.Lookup("error").Type().Underlying().(*types.Interface),
		errType:      types.Universe.Lookup("error").Type(),
		funcIndex:    make(map[*types.Func]funcEntry),
		funcCache:    make(map[*types.Func]map[string]bool),
		visited:      make(map[*types.Func]bool),
	}
}

// printFileErrors analyzes every function in file and prints the concrete
// error types each one can return.
func (a *analyzer) printFileErrors(file *ast.File, info *types.Info) {
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}

		errs := a.analyzeFunc(fd, info)
		if len(errs) == 0 {
			continue
		}

		name := fd.Name.Name
		if fd.Recv != nil && len(fd.Recv.List) > 0 {
			name = recvTypeName(fd.Recv.List[0].Type) + "." + name
		}

		fmt.Printf("func %s:\n", name)
		for _, e := range sortedKeys(errs) {
			fmt.Printf("  %s\n", e)
		}
	}
}

// indexPkg recursively indexes all function declarations in pkg and its deps.
func (a *analyzer) indexPkg(pkg *packages.Package) {
	if pkg == nil || pkg.TypesInfo == nil {
		return
	}

	// Check if already indexed by looking at whether any Defs from this pkg exist.
	for _, obj := range pkg.TypesInfo.Defs {
		if fn, ok := obj.(*types.Func); ok {
			if _, exists := a.funcIndex[fn]; exists {
				return // already indexed this package
			}

			break
		}
	}

	for _, f := range pkg.Syntax {
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}

			obj := pkg.TypesInfo.Defs[fd.Name]
			if fn, ok := obj.(*types.Func); ok {
				a.funcIndex[fn] = funcEntry{decl: fd, info: pkg.TypesInfo}
			}
		}
	}

	for _, dep := range pkg.Imports {
		a.indexPkg(dep)
	}
}

// analyzeFunc collects all concrete error types returned by fd, including
// those returned transitively by any functions it calls.
func (a *analyzer) analyzeFunc(fd *ast.FuncDecl, info *types.Info) map[string]bool {
	// If we have a types.Func for this, check cache.
	if obj := info.Defs[fd.Name]; obj != nil {
		if fn, ok := obj.(*types.Func); ok {
			return a.analyzeFuncObj(fn)
		}
	}

	return a.walkBody(fd, info)
}

// analyzeFuncObj resolves a [types.Func] to its source and collects
// concrete error types from its body. Results are cached, and cycles
// are detected via the visited set.
func (a *analyzer) analyzeFuncObj(fn *types.Func) map[string]bool {
	if cached, ok := a.funcCache[fn]; ok {
		return cached
	}

	if a.visited[fn] {
		return nil // cycle
	}

	entry, ok := a.funcIndex[fn]
	if !ok {
		return nil // no source available
	}

	a.visited[fn] = true

	result := a.walkBody(entry.decl, entry.info)

	delete(a.visited, fn)

	a.funcCache[fn] = result

	return result
}

// walkBody inspects every return statement and call expression in fd's
// body, collecting concrete error types returned directly or transitively.
func (a *analyzer) walkBody(fd *ast.FuncDecl, info *types.Info) map[string]bool {
	errs := make(map[string]bool)
	if fd.Body == nil {
		return errs
	}

	ast.Inspect(fd.Body, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.ReturnStmt:
			for _, expr := range node.Results {
				a.addTypedError(info.TypeOf(expr), errs)
			}
		case *ast.CallExpr:
			a.addCallErrors(node, info, errs)
		}

		return true
	})

	return errs
}

// addTypedError adds t to errs if it's a concrete type implementing error.
func (a *analyzer) addTypedError(t types.Type, errs map[string]bool) {
	if t == nil || types.Identical(t, a.errType) {
		return
	}

	if types.Implements(t, a.errInterface) {
		errs[t.String()] = true
	}
}

// addCallErrors inspects a call expression. If the callee's return signature
// includes a concrete error type, it's added directly. If it returns the bare
// error interface, we recurse into the callee's source to find concrete types.
func (a *analyzer) addCallErrors(call *ast.CallExpr, info *types.Info, errs map[string]bool) {
	// Check if any return type in the signature is a concrete error.
	t := info.TypeOf(call)
	if t == nil {
		return
	}

	hasBareErr := false

	collectFromType := func(rt types.Type) {
		if rt == nil {
			return
		}

		if types.Identical(rt, a.errType) {
			hasBareErr = true
			return
		}

		if types.Implements(rt, a.errInterface) {
			errs[rt.String()] = true
		}
	}

	if tuple, ok := t.(*types.Tuple); ok {
		for i := 0; i < tuple.Len(); i++ {
			collectFromType(tuple.At(i).Type())
		}
	} else {
		collectFromType(t)
	}

	// If the signature returns bare `error`, recurse into the callee.
	if !hasBareErr {
		return
	}

	callee := resolveCallee(call, info)
	if callee == nil {
		return
	}

	for e := range a.analyzeFuncObj(callee) {
		errs[e] = true
	}
}

// resolveCallee resolves a call expression to its *types.Func, if possible.
func resolveCallee(call *ast.CallExpr, info *types.Info) *types.Func {
	var obj types.Object

	switch fn := call.Fun.(type) {
	case *ast.Ident:
		obj = info.Uses[fn]
	case *ast.SelectorExpr:
		obj = info.Uses[fn.Sel]
	}

	if fn, ok := obj.(*types.Func); ok {
		return fn
	}

	return nil
}

// findFile locates the [ast.File] in pkg whose path matches target.
// It tries exact match first, then falls back to suffix matching.
func findFile(pkg *packages.Package, target string) *ast.File {
	for i, f := range pkg.GoFiles {
		if f == target || strings.HasSuffix(f, "/"+target) || strings.HasSuffix(f, "\\"+target) {
			return pkg.Syntax[i]
		}
	}

	for i, f := range pkg.GoFiles {
		if strings.HasSuffix(f, target) {
			return pkg.Syntax[i]
		}
	}

	return nil
}

// recvTypeName extracts the receiver type name from a method declaration's
// receiver expression, stripping pointer and generic index wrappers.
func recvTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return recvTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return recvTypeName(t.X)
	default:
		return ""
	}
}

// sortedKeys returns the keys of m in sorted order.
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))

	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}
