package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/plombardi89/maisys2/internal/config"
)

// typeDocs are the doc comments of a struct type and its exported fields.
type typeDocs struct {
	doc    string
	fields map[string]string
}

// moduleDocs parses the source of internal/config and of every package in
// internal/modules, and returns the doc comments of the struct types mods'
// configs are made of, and of their fields.
func moduleDocs(root string, mods []config.Module) (config.Docs, error) {
	configPkg := reflect.TypeFor[config.Docs]().PkgPath()
	modulesPkg := strings.TrimSuffix(configPkg, "config") + "modules"

	types := map[string]typeDocs{}
	if err := parseDir(filepath.Join(root, "internal", "config"), configPkg, types); err != nil {
		return nil, err
	}
	modulesDir := filepath.Join(root, "internal", "modules")
	entries, err := os.ReadDir(modulesDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "testdata" {
			continue
		}
		if err := parseDir(filepath.Join(modulesDir, e.Name()), modulesPkg+"/"+e.Name(), types); err != nil {
			return nil, err
		}
	}

	docs := config.Docs{}
	for key := range configTypes(mods) {
		td := types[key]
		if td.doc != "" {
			docs[key] = td.doc
		}
		for name, doc := range td.fields {
			docs[key+"."+name] = doc
		}
	}
	return docs, nil
}

// parseDir adds the docs of the struct types declared in the Go files in
// dir (leaving out tests), whose import path is importPath, to types. They
// are keyed by "<import path>.<TypeName>".
func parseDir(dir, importPath string, types map[string]typeDocs) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return err
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					continue
				}
				// A type declared on its own has its doc on the declaration.
				doc := ts.Doc
				if doc == nil && !gd.Lparen.IsValid() {
					doc = gd.Doc
				}
				td := typeDocs{doc: docText(doc), fields: map[string]string{}}
				for _, field := range st.Fields.List {
					text := docText(field.Doc)
					for _, n := range field.Names {
						if text != "" && n.IsExported() {
							td.fields[n.Name] = text
						}
					}
				}
				types[importPath+"."+ts.Name.Name] = td
			}
		}
	}
	return nil
}

func docText(doc *ast.CommentGroup) string {
	return strings.TrimSpace(doc.Text())
}

// configTypes returns the named struct types mods' configs are made of,
// keyed by "<import path>.<TypeName>".
func configTypes(mods []config.Module) map[string]bool {
	found := map[string]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		switch t.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array, reflect.Map:
			walk(t.Elem())
		case reflect.Struct:
			if t.Name() != "" {
				key := t.PkgPath() + "." + t.Name()
				if found[key] {
					return
				}
				found[key] = true
			}
			for f := range t.Fields() {
				walk(f.Type)
			}
		default:
		}
	}
	for _, mod := range mods {
		walk(reflect.TypeOf(mod.NewConfig()))
	}
	return found
}
