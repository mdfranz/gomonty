package monty

import (
	"context"
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestComplexAndModuleWireRoundTrip(t *testing.T) {
	negZero := math.Copysign(0, -1)
	values := []Value{
		Complex(complex(1.5, -2)),
		Complex(complex(negZero, 0)),
		Complex(complex(0, negZero)),
		ModuleValue(Module{
			Name: "pkg.sub",
			ID:   "12345678-9abc-4def-8123-456789abcdef",
			Attrs: Dict{
				{Key: String("answer"), Value: Int(42)},
				{Key: String("add"), Value: FunctionValue(Function{Name: "add"})},
			},
		}),
	}
	for _, original := range values {
		wire, err := wireValueFromPublic(original)
		if err != nil {
			t.Fatalf("wireValueFromPublic(%s): %v", original.Kind(), err)
		}
		decoded, err := wire.toPublic()
		if err != nil {
			t.Fatalf("toPublic(%s): %v", original.Kind(), err)
		}
		if !reflect.DeepEqual(decoded, original) {
			t.Fatalf("wire round-trip for %s = %#v, want %#v", original.Kind(), decoded, original)
		}
		// DeepEqual treats -0.0 == 0.0, so pin the sign bit separately.
		if c, ok := original.Complex(); ok {
			got, _ := decoded.Complex()
			if math.Signbit(real(got)) != math.Signbit(real(c)) || math.Signbit(imag(got)) != math.Signbit(imag(c)) {
				t.Fatalf("lost the sign of a zero part: got %v, want %v", got, c)
			}
		}
	}
}

func TestComplexAndModuleJSONRoundTrip(t *testing.T) {
	module := Module{
		Name:  "pkg",
		ID:    "12345678-9abc-4def-8123-456789abcdef",
		Attrs: Dict{{Key: String("answer"), Value: Int(42)}},
		Stub:  "answer: int",
	}
	for _, original := range []Value{Complex(complex(1, 2)), ModuleValue(module)} {
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatalf("Marshal(%s): %v", original.Kind(), err)
		}
		var decoded Value
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatalf("Unmarshal(%s): %v", original.Kind(), err)
		}
		if decoded.Kind() != original.Kind() {
			t.Fatalf("kind = %s, want %s", decoded.Kind(), original.Kind())
		}
	}
	if got, _ := ModuleValue(Module{Name: "x"}).Module(); got.ID == "" {
		t.Fatal("ModuleValue should generate an ID when none is given")
	}
}

func TestComplexString(t *testing.T) {
	for _, tc := range []struct {
		value complex128
		want  string
	}{
		{complex(0, 2), "2j"},
		{complex(1, 2), "(1+2j)"},
		{complex(1, -2.5), "(1-2.5j)"},
		{complex(math.Copysign(0, -1), 1), "(-0+1j)"},
		{complex(math.Inf(1), math.NaN()), "(inf+nanj)"},
	} {
		if got := Complex(tc.value).String(); got != tc.want {
			t.Errorf("Complex(%v).String() = %q, want %q", tc.value, got, tc.want)
		}
	}
}

func runCode(t *testing.T, code string, compile CompileOptions, opts RunOptions) (Value, error) {
	t.Helper()
	runner, err := New(code, compile)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer closeTestRunner(runner)
	return runner.Run(context.Background(), opts)
}

func TestComplexEndToEnd(t *testing.T) {
	got, err := runCode(t, "complex(1, 2) * 2", CompileOptions{ScriptName: "complex.py"}, RunOptions{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if c, ok := got.Complex(); !ok || c != complex(2, 4) {
		t.Fatalf("result = %v (%s), want (2+4j)", got, got.Kind())
	}

	got, err = runCode(t, "z + 1j", CompileOptions{ScriptName: "complex_input.py", Inputs: []string{"z"}},
		RunOptions{Inputs: map[string]Value{"z": Complex(complex(1, 1))}})
	if err != nil {
		t.Fatalf("Run with complex input: %v", err)
	}
	if c, _ := got.Complex(); c != complex(1, 2) {
		t.Fatalf("result = %v, want (1+2j)", got)
	}
}

func hostModules() map[string]Module {
	return map[string]Module{
		"hostmod": {
			Name: "hostmod",
			Attrs: Dict{
				{Key: String("answer"), Value: Int(42)},
				{Key: String("add"), Value: FunctionValue(Function{Name: "add"})},
			},
		},
	}
}

func TestHostModuleImport(t *testing.T) {
	got, err := runCode(t, `
import hostmod
from hostmod import answer
hostmod.add(answer, 1)
`, CompileOptions{ScriptName: "host_module.py"}, RunOptions{
		Modules: hostModules(),
		Functions: map[string]ExternalFunction{
			"add": func(_ context.Context, call Call) (Result, error) {
				return Return(Int(call.Args[0].Raw().(int64) + call.Args[1].Raw().(int64))), nil
			},
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Raw() != int64(43) {
		t.Fatalf("result = %v, want 43", got)
	}
}

func TestUnknownImportRaisesModuleNotFound(t *testing.T) {
	code := `
try:
    import nonexistent_module
    result = "imported"
except ModuleNotFoundError:
    result = "missing"
result
`
	// Without any OS handler, and with host modules that do not match.
	for name, opts := range map[string]RunOptions{
		"no handler":   {},
		"other module": {Modules: hostModules()},
	} {
		got, err := runCode(t, code, CompileOptions{ScriptName: "missing.py"}, opts)
		if err != nil {
			t.Fatalf("%s: Run: %v", name, err)
		}
		if got.Raw() != "missing" {
			t.Fatalf("%s: result = %v, want missing", name, got)
		}
	}
}

func TestImportFallsThroughToOSHandler(t *testing.T) {
	var asked []string
	got, err := runCode(t, "import dynamic\ndynamic.value", CompileOptions{ScriptName: "dynamic.py"}, RunOptions{
		Modules: hostModules(),
		OS: func(_ context.Context, call OSCall) (Result, error) {
			if call.Function != OSImport {
				t.Errorf("unexpected OS function %s", call.Function)
			}
			name := call.Args[0].Raw().(string)
			asked = append(asked, name)
			return Return(ModuleValue(Module{Name: name, Attrs: Dict{{Key: String("value"), Value: Int(7)}}})), nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Raw() != int64(7) || !reflect.DeepEqual(asked, []string{"dynamic"}) {
		t.Fatalf("result = %v, asked = %v", got, asked)
	}
}

func TestStdlibImportsDoNotReachHost(t *testing.T) {
	got, err := runCode(t, "import math\nimport hashlib\nmath.floor(2.5)", CompileOptions{ScriptName: "stdlib.py"}, RunOptions{
		OS: func(_ context.Context, call OSCall) (Result, error) {
			t.Errorf("stdlib import reached the host: %s %v", call.Function, call.Args)
			message := "unexpected"
			return Raise(Exception{Type: "RuntimeError", Arg: &message}), nil
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got.Raw() != int64(2) {
		t.Fatalf("result = %v, want 2", got)
	}
	if strings.Contains(got.String(), "module") {
		t.Fatalf("unexpected module result %s", got)
	}
}
