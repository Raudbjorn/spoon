package main

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

// TestProductionWritesUseTypedOutput is the canonical inventory of cmd/spn's
// actual writer boundaries and writer handoffs, including output.go. A new
// boundary fails until it receives a deliberate StreamKind disposition here.
func TestProductionWritesUseTypedOutput(t *testing.T) {
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(here)
	fset := token.NewFileSet()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		got = append(got, outputSites(file, fset)...)
	}
	sort.Strings(got)
	want := make([]string, 0, len(approvedOutputSites))
	for site, kind := range approvedOutputSites {
		if kind != StreamData && kind != StreamHuman && kind != StreamError {
			t.Fatalf("invalid stream classification for %s: %q", site, kind)
		}
		want = append(want, site+"|"+string(kind))
	}
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("unreviewed output boundary inventory:\n%s\n\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// Exact locations make a new same-named helper, imported writer, composite
// handoff, or direct write an explicit review event rather than an inherited
// default. Structured records and agentio envelopes are data; the four
// presentation routes identify the only human/error text currently emitted.
var approvedOutputSites = map[string]StreamKind{
	"main.go:101:call:writeError":    StreamError,
	"main.go:104:call:writeError":    StreamError,
	"main.go:57:call:Emit":           StreamData,
	"main.go:61:call:Emit":           StreamData,
	"main.go:64:call:Emit":           StreamData,
	"main.go:68:call:writeHuman":     StreamHuman,
	"main.go:71:call:writeHuman":     StreamHuman,
	"main.go:76:call:Emit":           StreamData,
	"output.go:110:call:WriteJSON":   StreamData,
	"output.go:114:call:WriteNDJSON": StreamData,
	"output.go:118:call:Emit":        StreamData,
	"output.go:124:call:NewWriter":   StreamData,
	"output.go:128:call:Write":       StreamData,
	"output.go:132:call:Flush":       StreamData,
	"output.go:138:return":           StreamData,
	"output.go:97:call:Write":        StreamData,
	"pr.go:46:call:Bootstrap":        StreamError,
	"pr.go:58:call:Emit":             StreamData,
	"pr.go:65:call:Emit":             StreamData,
	"pr.go:71:call:Emit":             StreamData,
	"pr.go:85:call:WriteJSON":        StreamData,
	"pr.go:86:call:Emit":             StreamData,
	"repo.go:101:call:Emit":          StreamData,
	"repo.go:107:call:Emit":          StreamData,
	"repo.go:110:call:Emit":          StreamData,
	"repo.go:115:call:Emit":          StreamData,
	"repo.go:123:call:Emit":          StreamData,
	"repo.go:125:call:WriteJSON":     StreamData,
	"repo.go:126:call:Emit":          StreamData,
	"repo.go:133:call:Emit":          StreamData,
	"repo.go:141:call:Emit":          StreamData,
	"repo.go:144:call:WriteJSON":     StreamData,
	"repo.go:145:call:Emit":          StreamData,
	"repo.go:54:call:Bootstrap":      StreamError,
	"repo.go:66:call:Emit":           StreamData,
	"repo.go:73:call:Emit":           StreamData,
	"repo.go:84:call:Emit":           StreamData,
	"repo.go:91:call:Emit":           StreamData,
	"repo.go:98:call:Emit":           StreamData,
	"search.go:108:call:Emit":        StreamData,
	"search.go:113:call:Emit":        StreamData,
	"search.go:119:call:Emit":        StreamData,
	"search.go:122:call:Emit":        StreamData,
	"search.go:128:call:Emit":        StreamData,
	"search.go:131:call:Emit":        StreamData,
	"search.go:137:call:Emit":        StreamData,
	"search.go:142:call:Emit":        StreamData,
	"search.go:151:call:Emit":        StreamData,
	"search.go:158:call:Emit":        StreamData,
	"search.go:167:call:Emit":        StreamData,
	"search.go:179:call:Emit":        StreamData,
	"search.go:186:call:Emit":        StreamData,
	"search.go:205:call:Emit":        StreamData,
	"search.go:207:call:Emit":        StreamData,
	"search.go:211:call:Emit":        StreamData,
	"search.go:231:call:Emit":        StreamData,
	"search.go:382:call:WriteNDJSON": StreamData,
	"search.go:399:call:WriteNDJSON": StreamData,
	"search.go:410:call:WriteNDJSON": StreamData,
	"search.go:77:call:Emit":         StreamData,
	"search.go:82:call:Emit":         StreamData,
	"search.go:88:call:Emit":         StreamData,
	"search.go:92:call:Emit":         StreamData,
	"search.go:97:call:Emit":         StreamData,
	"threads.go:108:call:Emit":       StreamData,
	"threads.go:117:call:Emit":       StreamData,
	"threads.go:121:call:Emit":       StreamData,
	"threads.go:124:call:Emit":       StreamData,
	"threads.go:130:call:Emit":       StreamData,
	"threads.go:142:call:Emit":       StreamData,
	"threads.go:149:call:Emit":       StreamData,
	"threads.go:175:call:WriteJSON":  StreamData,
	"threads.go:176:call:Emit":       StreamData,
	"threads.go:238:call:Emit":       StreamData,
	"threads.go:247:call:Emit":       StreamData,
	"threads.go:251:call:Emit":       StreamData,
	"threads.go:254:call:Emit":       StreamData,
	"threads.go:260:call:Emit":       StreamData,
	"threads.go:279:call:WriteNull":  StreamData,
	"threads.go:285:call:WriteJSON":  StreamData,
	"threads.go:286:call:Emit":       StreamData,
	"threads.go:299:call:Emit":       StreamData,
	"threads.go:314:call:Emit":       StreamData,
	"threads.go:320:call:Emit":       StreamData,
	"threads.go:326:call:Emit":       StreamData,
	"threads.go:333:call:Emit":       StreamData,
	"threads.go:340:call:Emit":       StreamData,
	"threads.go:347:call:Emit":       StreamData,
	"threads.go:354:call:Emit":       StreamData,
	"threads.go:359:call:Emit":       StreamData,
	"threads.go:363:call:Emit":       StreamData,
	"threads.go:366:call:Emit":       StreamData,
	"threads.go:369:call:Emit":       StreamData,
	"threads.go:374:call:Emit":       StreamData,
	"threads.go:385:call:Emit":       StreamData,
	"threads.go:390:call:Emit":       StreamData,
	"threads.go:404:call:WriteJSON":  StreamData,
	"threads.go:405:call:Emit":       StreamData,
	"threads.go:420:call:Emit":       StreamData,
	"threads.go:426:call:Emit":       StreamData,
	"threads.go:438:call:Emit":       StreamData,
	"threads.go:447:call:Emit":       StreamData,
	"threads.go:452:call:Emit":       StreamData,
	"threads.go:459:call:Emit":       StreamData,
	"threads.go:46:call:Bootstrap":   StreamError,
	"threads.go:464:call:Emit":       StreamData,
	"threads.go:469:call:Emit":       StreamData,
	"threads.go:503:call:WriteJSON":  StreamData,
	"threads.go:504:call:Emit":       StreamData,
	"threads.go:521:call:Emit":       StreamData,
	"threads.go:524:call:Emit":       StreamData,
	"threads.go:530:call:Emit":       StreamData,
	"threads.go:548:call:WriteJSON":  StreamData,
	"threads.go:549:call:Emit":       StreamData,
	"threads.go:563:call:Emit":       StreamData,
	"threads.go:566:call:Emit":       StreamData,
	"threads.go:572:call:Emit":       StreamData,
	"threads.go:58:call:Emit":        StreamData,
	"threads.go:586:call:WriteJSON":  StreamData,
	"threads.go:587:call:Emit":       StreamData,
	"threads.go:615:call:Emit":       StreamData,
	"threads.go:620:call:Emit":       StreamData,
	"threads.go:629:call:Emit":       StreamData,
	"threads.go:635:call:Emit":       StreamData,
	"threads.go:642:call:Emit":       StreamData,
	"threads.go:647:call:Emit":       StreamData,
	"threads.go:669:call:Emit":       StreamData,
	"threads.go:673:call:Emit":       StreamData,
	"threads.go:676:call:Emit":       StreamData,
	"threads.go:708:call:WriteJSON":  StreamData,
	"threads.go:709:call:Emit":       StreamData,
	"threads.go:789:call:Emit":       StreamData,
	"threads.go:79:call:Emit":        StreamData,
	"threads.go:794:call:Emit":       StreamData,
	"threads.go:799:call:Emit":       StreamData,
	"threads.go:804:call:Emit":       StreamData,
	"threads.go:808:call:Emit":       StreamData,
	"threads.go:811:call:Emit":       StreamData,
	"threads.go:818:call:Emit":       StreamData,
	"threads.go:822:call:Emit":       StreamData,
	"threads.go:830:call:Emit":       StreamData,
	"threads.go:844:call:Emit":       StreamData,
	"threads.go:846:call:WriteJSON":  StreamData,
	"threads.go:847:call:Emit":       StreamData,
	"threads.go:98:call:Emit":        StreamData,
	"eval.go:58:call:Emit":           StreamData,
	"eval.go:64:call:Emit":           StreamData,
	"eval.go:70:call:Emit":           StreamData,
	"eval.go:75:call:Emit":           StreamData,
	"eval.go:79:call:Emit":           StreamData,
	"eval.go:84:call:Emit":           StreamData,
	"eval.go:89:call:Emit":           StreamData,
	"eval.go:94:call:Emit":           StreamData,
	"eval.go:101:call:Emit":          StreamData,
	"eval.go:106:call:Emit":          StreamData,
	"eval.go:111:call:Emit":          StreamData,
	"eval.go:117:call:Emit":          StreamData,
	"eval.go:129:call:Emit":          StreamData,
	"eval.go:132:call:Emit":          StreamData,
	"eval.go:138:call:Emit":          StreamData,
	"eval.go:141:call:Emit":          StreamData,
	"eval.go:148:call:Emit":          StreamData,
	"eval.go:152:call:Emit":          StreamData,
	"eval.go:155:call:Emit":          StreamData,
	"eval.go:158:call:Emit":          StreamData,
	"eval.go:167:call:Emit":          StreamData,
	"eval.go:171:call:Emit":          StreamData,
	"eval.go:203:call:Emit":          StreamData,
	"eval.go:211:call:Emit":          StreamData,
	"eval.go:213:call:Emit":          StreamData,
	"eval.go:247:call:Emit":          StreamData,
	"forks.go:114:call:Emit":         StreamData,
	"forks.go:121:call:Emit":         StreamData,
	"forks.go:198:call:Emit":         StreamData,
	"forks.go:203:call:Emit":         StreamData,
	"forks.go:217:call:Emit":         StreamData,
	"forks.go:222:call:Emit":         StreamData,
	"forks.go:235:call:Emit":         StreamData,
	"forks.go:240:call:Emit":         StreamData,
	"forks.go:245:call:Emit":         StreamData,
	"forks.go:250:call:Emit":         StreamData,
	"forks.go:255:call:Emit":         StreamData,
	"forks.go:260:call:Emit":         StreamData,
	"forks.go:265:call:Emit":         StreamData,
	"forks.go:270:call:Emit":         StreamData,
	"forks.go:275:call:Emit":         StreamData,
	"forks.go:282:call:Emit":         StreamData,
	"forks.go:291:call:Emit":         StreamData,
	"forks.go:296:call:Emit":         StreamData,
	"forks.go:302:call:Emit":         StreamData,
	"forks.go:311:call:Emit":         StreamData,
	"forks.go:317:call:Emit":         StreamData,
	"forks.go:325:call:Emit":         StreamData,
	"forks.go:330:call:Emit":         StreamData,
	"forks.go:335:call:Emit":         StreamData,
	"forks.go:340:call:Emit":         StreamData,
	"forks.go:346:call:Emit":         StreamData,
	"forks.go:351:call:Emit":         StreamData,
	"forks.go:356:call:Emit":         StreamData,
	"forks.go:361:call:Emit":         StreamData,
	"forks.go:366:call:Emit":         StreamData,
	"forks.go:373:call:Emit":         StreamData,
	"forks.go:378:call:Emit":         StreamData,
	"forks.go:384:call:Emit":         StreamData,
	"forks.go:389:call:Emit":         StreamData,
	"forks.go:394:call:Emit":         StreamData,
	"forks.go:399:call:Emit":         StreamData,
	"forks.go:404:call:Emit":         StreamData,
	"forks.go:410:call:Emit":         StreamData,
	"forks.go:440:call:Emit":         StreamData,
	"forks.go:448:call:Emit":         StreamData,
	"forks.go:453:call:Emit":         StreamData,
	"forks.go:459:call:Emit":         StreamData,
	"forks.go:468:call:Emit":         StreamData,
	"forks.go:472:call:Emit":         StreamData,
	"forks.go:477:call:Emit":         StreamData,
	"forks.go:482:call:Emit":         StreamData,
	"forks.go:487:call:Emit":         StreamData,
	"forks.go:492:call:Emit":         StreamData,
	"forks.go:497:call:Emit":         StreamData,
	"forks.go:502:call:Emit":         StreamData,
	"forks.go:507:call:Emit":         StreamData,
	"forks.go:514:call:Emit":         StreamData,
	"forks.go:517:call:Emit":         StreamData,
	"forks.go:526:call:Emit":         StreamData,
	"forks.go:529:call:Emit":         StreamData,
	"forks.go:535:call:Emit":         StreamData,
	"forks.go:547:call:Emit":         StreamData,
	"forks.go:552:call:Emit":         StreamData,
	"forks.go:555:call:Emit":         StreamData,
	"forks.go:558:call:Emit":         StreamData,
	"forks.go:561:call:Emit":         StreamData,
	"forks.go:564:call:Emit":         StreamData,
	"forks.go:568:call:Emit":         StreamData,
	"forks.go:571:call:Emit":         StreamData,
	"forks.go:574:call:Emit":         StreamData,
	"forks.go:585:call:Emit":         StreamData,
	"forks.go:592:call:Emit":         StreamData,
	"forks.go:637:call:WriteNDJSON":  StreamData,
	"forks.go:713:call:WriteNDJSON":  StreamData,
	"forks.go:723:call:Emit":         StreamData,
	"forks.go:761:call:Emit":         StreamData,
	"forks.go:780:call:Emit":         StreamData,
	"forks.go:790:call:Emit":         StreamData,
	"forks.go:802:call:WriteNDJSON":  StreamData,
	"forks.go:831:call:Emit":         StreamData,
	"forks.go:862:call:Emit":         StreamData,
	"forks.go:875:call:Emit":         StreamData,
	"forks.go:896:call:Emit":         StreamData,
	"forks.go:926:call:Emit":         StreamData,
	"forks.go:930:call:WriteNDJSON":  StreamData,
	"forks.go:1106:call:WriteNDJSON": StreamData,
	"forks.go:1134:call:WriteNDJSON": StreamData,
	"forks.go:1157:call:WriteNDJSON": StreamData,
	"forks.go:1173:call:WriteNDJSON": StreamData,
	"forks.go:1190:call:WriteNDJSON": StreamData,
	"forks.go:1251:call:WriteNDJSON": StreamData,
	"forks.go:1259:call:WriteNDJSON": StreamData,
	"forks.go:1614:call:Emit":        StreamData,
	"forks.go:1632:call:WriteNDJSON": StreamData,
	"forks.go:1643:call:Emit":        StreamData,
	"forks.go:1647:call:Emit":        StreamData,
	"forks.go:1726:call:WriteNDJSON": StreamData,
	"forks.go:1769:call:WriteNDJSON": StreamData,
	"forks.go:1780:call:WriteNDJSON": StreamData,
	"forks.go:1810:call:WriteNDJSON": StreamData,
	"forks.go:1835:call:WriteNDJSON": StreamData,
	"forks.go:1854:call:WriteNDJSON": StreamData,
	"forks.go:1913:call:Emit":        StreamData,
	"forks.go:1927:call:WriteNDJSON": StreamData,
	"forks.go:1967:call:Emit":        StreamData,
	"forks.go:1971:call:WriteNDJSON": StreamData,
}

func TestOutputInventoryRejectsNegativeFixtures(t *testing.T) {
	for _, source := range []string{
		`package p; import "io"; func f(w io.Writer) { w.Write([]byte("x")) }`,
		`package p; import "io"; type Options struct { Logger io.Writer }; func f(stderr io.Writer) { _ = Options{Logger: stderr} }`,
		`package p; import ioAlias "io"; func handoff(stderr ioAlias.Writer) ioAlias.Writer { return stderr }`,
		`package p; import "fmt"; import "os"; func f() { fmt.Fprintln(os.Stdout, "raw") }`,
		`package p; import "io"; import external "example.test/external"; func f(stderr io.Writer) { external.Log(stderr) }`,
		`package p; import "io"; type E struct{}; func (E) Emit(io.Writer) {}; func f(stderr io.Writer) { E{}.Emit(stderr) }`,
		`package p; import "io"; import external "example.test/external"; func f(stderr io.Writer) { e := external.New(); e.Emit(stderr) }`,
		`package p; import "io"; type W = io.Writer; func f(stderr W) { stderr.Write([]byte("x")) }`,
		`package p; import "io"; import external "example.test/external"; func f(stderr io.Writer) { var e external.E; e.Emit(stderr) }`,
		`package p; import "io"; func outer(stderr io.Writer) { func(w io.Writer) { w.Write([]byte("x")) }(stderr) }`,
		`package p; import "io"; func outer() { func(w io.Writer) { x := w; x.Write([]byte("x")) }(nil) }`,
	} {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "output.go", source, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := outputSites(file, fset); len(got) == 0 {
			t.Fatalf("fixture escaped output inventory: %s", source)
		}
	}
}

func outputSites(file *ast.File, fset *token.FileSet) []string {
	imports := importAliases(file)
	ioNames := imports["io"]
	writerTypes := writerTypeNames(file, ioNames)
	var sites []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		sites = append(sites, outputSitesInBlock(
			fn.Body,
			writerParameters(fn, ioNames, writerTypes),
			ioNames,
			writerTypes,
			fset,
		)...)
	}
	return sites
}
func outputSitesInBlock(body *ast.BlockStmt, writers map[string]bool, ioNames, writerTypes map[string]bool, fset *token.FileSet) []string {
	propagateWriterAliases(body, writers)
	var sites []string
	ast.Inspect(body, func(node ast.Node) bool {
		if literal, ok := node.(*ast.FuncLit); ok {
			literalWriters := cloneWriterSet(writers)
			addWriterParameters(literalWriters, literal.Type.Params, ioNames, writerTypes)
			sites = append(sites, outputSitesInBlock(
				literal.Body,
				literalWriters,
				ioNames,
				writerTypes,
				fset,
			)...)
			return false
		}
		form := ""
		kind := StreamData
		switch node := node.(type) {
		case *ast.AssignStmt:
			for i, rhs := range node.Rhs {
				if i < len(node.Lhs) && writerValue(rhs, writers) {
					if _, alias := node.Lhs[i].(*ast.Ident); !alias {
						form = "assignment"
					}
				}
			}
		case *ast.CompositeLit:
			for _, elt := range node.Elts {
				if value, ok := elt.(*ast.KeyValueExpr); ok && writerValue(value.Value, writers) {
					form = "composite"
				}
			}
		case *ast.ReturnStmt:
			for _, result := range node.Results {
				if writerValue(result, writers) {
					form = "return"
				}
			}
		case *ast.CallExpr:
			if presentationKind, ok := presentationCallKind(node); ok {
				form, kind = "call:"+callName(node), presentationKind
			} else if isWriterBoundaryCall(node, writers) {
				form = "call:" + callName(node)
				if callName(node) == "Bootstrap" {
					kind = StreamError
				}
			}
		}
		if form != "" {
			position := fset.Position(node.Pos())
			sites = append(sites, filepath.Base(position.Filename)+":"+strconv.Itoa(position.Line)+":"+form+"|"+string(kind))
		}
		return true
	})
	return sites
}

func importAliases(file *ast.File) map[string]map[string]bool {
	imports := make(map[string]map[string]bool)
	for _, spec := range file.Imports {
		path, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(path)
		if spec.Name != nil {
			name = spec.Name.Name
		}
		if name == "_" || name == "." {
			continue
		}
		if imports[path] == nil {
			imports[path] = make(map[string]bool)
		}
		imports[path][name] = true
	}
	return imports
}
func writerParameters(fn *ast.FuncDecl, ioNames, writerTypes map[string]bool) map[string]bool {
	return writerParametersFromFields(fn.Type.Params, ioNames, writerTypes)
}

func writerParametersFromFields(params *ast.FieldList, ioNames, writerTypes map[string]bool) map[string]bool {
	writers := make(map[string]bool)
	addWriterParameters(writers, params, ioNames, writerTypes)
	return writers
}

func addWriterParameters(writers map[string]bool, params *ast.FieldList, ioNames, writerTypes map[string]bool) {
	if params == nil {
		return
	}
	for _, field := range params.List {
		if !isWriterType(field.Type, ioNames, writerTypes) {
			continue
		}
		for _, name := range field.Names {
			writers[name.Name] = true
		}
	}
}

func writerTypeNames(file *ast.File, ioNames map[string]bool) map[string]bool {
	names := make(map[string]bool)
	for changed := true; changed; {
		changed = false
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || names[typeSpec.Name.Name] || !isWriterType(typeSpec.Type, ioNames, names) {
					continue
				}
				names[typeSpec.Name.Name] = true
				changed = true
			}
		}
	}
	return names
}

func cloneWriterSet(writers map[string]bool) map[string]bool {
	cloned := make(map[string]bool, len(writers))
	for name := range writers {
		cloned[name] = true
	}
	return cloned
}

func isWriterType(expr ast.Expr, ioNames, writerTypes map[string]bool) bool {
	switch expr := expr.(type) {
	case *ast.Ident:
		return writerTypes[expr.Name]
	case *ast.SelectorExpr:
		packageName, ok := expr.X.(*ast.Ident)
		return ok && expr.Sel.Name == "Writer" && ioNames[packageName.Name]
	case *ast.InterfaceType:
		if expr.Methods == nil {
			return false
		}
		for _, method := range expr.Methods.List {
			for _, name := range method.Names {
				if name.Name == "Write" {
					return true
				}
			}
		}
	}
	return false
}

func propagateWriterAliases(body *ast.BlockStmt, writers map[string]bool) {
	for changed := true; changed; {
		changed = false
		ast.Inspect(body, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.AssignStmt:
				for i, rhs := range node.Rhs {
					if i >= len(node.Lhs) || !writerValue(rhs, writers) {
						continue
					}
					if lhs, ok := node.Lhs[i].(*ast.Ident); ok && !writers[lhs.Name] {
						writers[lhs.Name] = true
						changed = true
					}
				}
			case *ast.ValueSpec:
				for i, rhs := range node.Values {
					if i >= len(node.Names) || !writerValue(rhs, writers) || writers[node.Names[i].Name] {
						continue
					}
					writers[node.Names[i].Name] = true
					changed = true
				}
			}
			return true
		})
	}
}

func writerValue(expr ast.Expr, writers map[string]bool) bool {
	ident, ok := expr.(*ast.Ident)
	return ok && writers[ident.Name]
}

func presentationCallKind(call *ast.CallExpr) (StreamKind, bool) {
	name, ok := call.Fun.(*ast.Ident)
	if !ok {
		return "", false
	}
	switch name.Name {
	case "writeHuman":
		return StreamHuman, true
	case "writeError":
		return StreamError, true
	default:
		return "", false
	}
}

func isWriterBoundaryCall(call *ast.CallExpr, writers map[string]bool) bool {
	if selector, ok := call.Fun.(*ast.SelectorExpr); ok && writerValue(selector.X, writers) {
		return true
	}
	if directWriteMethod(call) {
		return true
	}
	for _, arg := range call.Args {
		if writerValue(arg, writers) || isOSStream(arg) {
			return isExternalCall(call)
		}
	}
	return false
}

func directWriteMethod(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok || (selector.Sel.Name != "Write" && selector.Sel.Name != "Flush") {
		return false
	}
	_, nested := selector.X.(*ast.SelectorExpr)
	return nested
}

func isExternalCall(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false // local helpers are inspected at their terminal boundary.
	}
	switch receiver := selector.X.(type) {
	case *ast.Ident:
		// A selector method supplied a tracked writer. Whether receiver is an
		// import alias or a local external value, its method is a boundary;
		// package-local forwarding remains an Ident function call and is
		// inspected at its own terminal implementation.
		_ = receiver
		return true
	case *ast.CallExpr, *ast.CompositeLit:
		return true // e.g. agentio.NewError(...).Emit(stderr) or E{}.Emit(stderr)
	}
	return false
}

func isOSStream(expr ast.Expr) bool {
	selector, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	packageName, ok := selector.X.(*ast.Ident)
	return ok && packageName.Name == "os" && (selector.Sel.Name == "Stdout" || selector.Sel.Name == "Stderr")
}

func callName(call *ast.CallExpr) string {
	switch fun := call.Fun.(type) {
	case *ast.Ident:
		return fun.Name
	case *ast.SelectorExpr:
		return fun.Sel.Name
	default:
		return "writer-call"
	}
}
