package monty

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestValueNativeJSON(t *testing.T) {
	v := List(
		None(), Bool(true), Int(1), Float(1.5), String("s"),
		TupleValue(Int(2), String("x")),
		DictValue(Dict{
			{Key: String("a"), Value: Int(1)},
			{Key: Int(2), Value: List(Int(3))},
		}),
		PathValue(Path("/tmp")),
	)
	got, err := json.Marshal(v.Native())
	if err != nil {
		t.Fatal(err)
	}
	want := `[null,true,1,1.5,"s",[2,"x"],{"2":[3],"a":1},"/tmp"]`
	if string(got) != want {
		t.Errorf("Native JSON = %s, want %s", got, want)
	}
	if !(Value{}).IsNone() || !None().IsNone() || Int(0).IsNone() {
		t.Error("IsNone mismatch")
	}
}

// TestValueKindConstantsExported fails if a ValueKind constant is added
// without being exported (issue #45).
func TestValueKindConstantsExported(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "types.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			if id, ok := vs.Type.(*ast.Ident); !ok || id.Name != "ValueKind" {
				continue
			}
			for _, name := range vs.Names {
				n++
				if !strings.HasPrefix(name.Name, "ValueKind") {
					t.Errorf("ValueKind constant %q must be exported as ValueKind*", name.Name)
				}
			}
		}
	}
	if n == 0 {
		t.Fatal("found no ValueKind constants")
	}
}
