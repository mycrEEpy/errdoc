package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/token"
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

const errDocPrefix = "// Returns errors:"

func main() {
	writeFlag := flag.Bool("w", false, "write error types into function doc comments")
	flag.Usage = func() {
		fmt.Printf("usage: errdoc [-w] <file.go|directory>\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}
	target := flag.Arg(0)

	cfg := &packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedSyntax |
			packages.NeedTypes | packages.NeedTypesInfo | packages.NeedDeps | packages.NeedImports,
	}

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

	if *writeFlag {
		a.writeFileErrors(files, pkg)
	} else {
		for _, file := range files {
			a.printFileErrors(file, pkg.TypesInfo)
		}
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

// writeFileErrors analyzes every function in the given files and writes
// the concrete error types into each function's doc comment. Existing
// "Returns errors:" blocks are replaced; new ones are inserted.
// funcResult pairs a function declaration with its sorted error type names.
type funcResult struct {
	decl *ast.FuncDecl
	errs []string
}

func (a *analyzer) writeFileErrors(files []*ast.File, pkg *packages.Package) {
	byFile := make(map[string][]funcResult)

	for _, file := range files {
		filePath := pkg.Fset.Position(file.Pos()).Filename
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			errs := a.analyzeFunc(fd, pkg.TypesInfo)
			byFile[filePath] = append(byFile[filePath], funcResult{decl: fd, errs: sortedKeys(errs)})
		}
	}

	for filePath, results := range byFile {
		src, err := os.ReadFile(filePath)
		if err != nil {
			fmt.Printf("read %s: %v\n", filePath, err)
			continue
		}
		src = rewriteSource(src, results, pkg.Fset)
		if err := os.WriteFile(filePath, src, 0644); err != nil {
			fmt.Printf("write %s: %v\n", filePath, err)
		}
	}
}

// rewriteSource applies error doc edits to src, processing functions from
// bottom to top so that earlier byte offsets remain valid.
func rewriteSource(src []byte, results []funcResult, fset *token.FileSet) []byte {
	// Process from bottom to top to preserve offsets.
	for i := len(results) - 1; i >= 0; i-- {
		r := results[i]
		src = rewriteFuncDoc(src, r.decl, r.errs, fset)
	}
	return src
}

// rewriteFuncDoc updates or inserts the "Returns errors:" block in the
// doc comment of a single function declaration.
func rewriteFuncDoc(src []byte, fd *ast.FuncDecl, errs []string, fset *token.FileSet) []byte {
	errBlock := buildErrBlock(errs)
	funcOff := fset.Position(fd.Pos()).Offset

	// Scan backwards from the func keyword to find an existing error block
	// in the raw source, independent of AST doc comment attachment.
	blockStart, blockEnd := findErrBlockBytes(src, funcOff)
	if blockStart >= 0 {
		if errBlock == "" {
			// Remove the block and the following newline.
			removeEnd := blockEnd
			if removeEnd < len(src) && src[removeEnd] == '\n' {
				removeEnd++
			}
			return splice(src, blockStart, removeEnd, "")
		}
		return splice(src, blockStart, blockEnd, errBlock)
	}

	if errBlock == "" {
		return src
	}

	if fd.Doc != nil && len(fd.Doc.List) > 0 {
		// Append after last doc comment line.
		last := fd.Doc.List[len(fd.Doc.List)-1]
		insertOff := fset.Position(last.End()).Offset
		return splice(src, insertOff, insertOff, "\n"+errBlock)
	}

	// No existing doc comment — insert before the func keyword.
	return splice(src, funcOff, funcOff, errBlock+"\n")
}

// buildErrBlock builds the "// Returns errors:" comment block text.
// Returns an empty string if there are no errors.
func buildErrBlock(errs []string) string {
	if len(errs) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(errDocPrefix)
	b.WriteString("\n//")
	for _, e := range errs {
		b.WriteString("\n//   ")
		b.WriteString(e)
	}
	return b.String()
}

// findErrBlockBytes scans the raw source backwards from funcOff to find
// an existing "Returns errors:" block. It returns the byte range
// [start, end) covering the entire block, or -1, -1 if not found.
func findErrBlockBytes(src []byte, funcOff int) (int, int) {
	// Walk backwards over comment lines immediately preceding the func.
	lines := strings.Split(string(src[:funcOff]), "\n")
	// Drop the last element (the func line or empty trailing split).
	if len(lines) > 0 {
		lines = lines[:len(lines)-1]
	}

	// Scan upward from the bottom collecting the error block lines.
	blockEnd := -1
	blockStart := -1
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" {
			break // blank line means end of contiguous comment
		}
		if strings.HasPrefix(trimmed, "//   ") || trimmed == "//" {
			if blockEnd < 0 {
				blockEnd = i
			}
			blockStart = i
			continue
		}
		if strings.HasPrefix(trimmed, errDocPrefix) {
			blockStart = i
			if blockEnd < 0 {
				blockEnd = i
			}
			break
		}
		break // some other comment line, stop
	}

	if blockStart < 0 {
		return -1, -1
	}

	// Verify the block actually starts with the prefix.
	if !strings.HasPrefix(strings.TrimSpace(lines[blockStart]), errDocPrefix) {
		return -1, -1
	}

	// Convert line indices back to byte offsets.
	startOff := 0
	for i := 0; i < blockStart; i++ {
		startOff += len(lines[i]) + 1 // +1 for '\n'
	}
	endOff := startOff
	for i := blockStart; i <= blockEnd; i++ {
		endOff += len(lines[i]) + 1
	}
	// Exclude the final newline so the splice preserves the line break
	// between the block and the func keyword.
	if endOff > startOff {
		endOff--
	}

	return startOff, endOff
}

// splice replaces src[start:end] with replacement.
func splice(src []byte, start, end int, replacement string) []byte {
	var b []byte
	b = append(b, src[:start]...)
	b = append(b, replacement...)
	b = append(b, src[end:]...)
	return b
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

// resolveCallee resolves a call expression to its [types.Func], if possible.
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
