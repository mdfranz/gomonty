package monty

import (
	"context"
	"reflect"
	"testing"
)

func TestUpstreamRefreshWireValuesRoundTrip(t *testing.T) {
	offset := int32(-18000)
	timezoneName := "EST"
	values := []Value{
		NotImplemented(),
		TimeValue(Time{Hour: 1, Minute: 2, Second: 3, Microsecond: 4, OffsetSeconds: &offset, TimezoneName: &timezoneName, Fold: 1}),
		ClassInstanceValue(ClassInstance{
			ClassType: ClassType{
				Name:        "Config",
				ID:          "12345678-9abc-4def-8123-456789abcdef",
				HostDefined: true,
				IsDataclass: true,
				Attrs:       Dict{{Key: String("version"), Value: Int(1)}},
			},
			InstanceID: "fedcba98-7654-4321-8fed-cba987654321",
			Attrs:      Dict{{Key: String("enabled"), Value: Bool(true)}},
		}),
		// FileHandle round-trips at the Go wire layer like the pre-existing
		// output-only kinds (Repr, Cycle) do — wireValueFromPublic/toPublic
		// don't enforce input/output directionality on either side; only
		// Rust's into_monty does (see TestFileHandleRejectedAsRunnerInput).
		FileHandleValue(FileHandle{Path: "/tmp/example.txt", Mode: "r", Position: 42}),
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
	}
}

// TestFileHandleRejectedAsRunnerInput exercises the real enforcement point
// for "file handles are output-only" end to end: Go's wireValueFromPublic
// happily encodes one (see TestUpstreamRefreshWireValuesRoundTrip), so the
// rejection has to come from the Rust side's into_monty when the runner
// actually starts.
func TestFileHandleRejectedAsRunnerInput(t *testing.T) {
	runner, err := New("f", CompileOptions{ScriptName: "filehandle.py", Inputs: []string{"f"}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() {
		closeTestRunner(runner)
	})

	_, err = runner.Run(context.Background(), RunOptions{
		Inputs: map[string]Value{"f": FileHandleValue(FileHandle{Path: "/tmp/example.txt", Mode: "r"})},
	})
	if err == nil {
		t.Fatal("Run succeeded with a FileHandle input; file handles are output-only")
	}
}

func TestNewWireResourceLimits(t *testing.T) {
	limits := &ResourceLimits{MaxRecursionDepth: 64, MaxSuspensions: 12}
	wire := newWireResourceLimits(limits)
	if wire.MaxRecursionDepth == nil || *wire.MaxRecursionDepth != 64 {
		t.Fatalf("MaxRecursionDepth = %#v, want 64", wire.MaxRecursionDepth)
	}
	if wire.MaxSuspensions == nil || *wire.MaxSuspensions != 12 {
		t.Fatalf("MaxSuspensions = %#v, want 12", wire.MaxSuspensions)
	}

	zero := newWireResourceLimits(&ResourceLimits{})
	if zero.MaxSuspensions != nil {
		t.Fatalf("zero MaxSuspensions = %#v, want nil so upstream applies its default", zero.MaxSuspensions)
	}
}
