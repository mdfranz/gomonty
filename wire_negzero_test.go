package monty

import (
	"math"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

// TestFloatNegativeZeroSurvivesWire pins the sign bit of -0.0 through the
// MessagePack encoding (issue #74). omitempty would drop it as a zero value.
func TestFloatNegativeZeroSurvivesWire(t *testing.T) {
	for _, in := range []float64{math.Copysign(0, -1), 0, 1.5} {
		wire, err := wireValueFromPublic(Float(in))
		if err != nil {
			t.Fatal(err)
		}
		data, err := msgpack.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		var back wireValue
		if err := msgpack.Unmarshal(data, &back); err != nil {
			t.Fatal(err)
		}
		v, err := back.toPublic()
		if err != nil {
			t.Fatal(err)
		}
		got, _ := v.Raw().(float64)
		if got != in || math.Signbit(got) != math.Signbit(in) {
			t.Errorf("float %v (signbit %v) round-tripped to %v (signbit %v)", in, math.Signbit(in), got, math.Signbit(got))
		}
	}
}
