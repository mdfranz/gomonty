package monty

import "fmt"

// IsNone reports whether v is Python None (including the zero Value).
func (v Value) IsNone() bool {
	return v.Kind() == ValueKindNone
}

// Native converts v into plain Go values that encoding/json can marshal
// directly, without the {"kind": ..., "value": ...} envelope that
// [Value.MarshalJSON] emits. MarshalJSON stays the lossless form; Native is
// the convenient, lossy one (for example for returning a script result to an
// LLM tool call).
//
// The mapping is:
//
//   - None -> nil
//   - bool -> bool, int -> int64, big int -> *big.Int, float -> float64,
//     str -> string, bytes -> []byte
//   - list, tuple, set, frozenset -> []any (set order is as received)
//   - dict -> map[string]any. String keys are used as is; any other key is
//     rendered with [Value.String]. Insertion order is lost, and keys that
//     render identically collapse to the last one.
//   - named tuple -> map[string]any keyed by field name
//   - every other kind (ellipsis, NotImplemented, complex, decimal, path,
//     date and time kinds, exceptions, functions, modules, class instances,
//     file handles, repr and cycle placeholders) -> its [Value.String] text
//
// Floats are returned as is, so NaN and the infinities are not representable
// in JSON and make json.Marshal fail.
func (v Value) Native() any {
	switch v.Kind() {
	case ValueKindNone:
		return nil
	case ValueKindBool, ValueKindInt, ValueKindBigInt, ValueKindFloat, ValueKindString, ValueKindBytes:
		return v.data
	case ValueKindList:
		return nativeItems(v.data.([]Value))
	case ValueKindTuple:
		return nativeItems(v.data.(Tuple))
	case ValueKindSet:
		return nativeItems(v.data.(Set))
	case ValueKindFrozenSet:
		return nativeItems(v.data.(FrozenSet))
	case ValueKindDict:
		d := v.data.(Dict)
		out := make(map[string]any, len(d))
		for _, p := range d {
			out[nativeKey(p.Key)] = p.Value.Native()
		}
		return out
	case ValueKindNamedTuple:
		nt := v.data.(NamedTuple)
		out := make(map[string]any, len(nt.Values))
		for i, item := range nt.Values {
			name := fmt.Sprintf("%d", i)
			if i < len(nt.FieldNames) {
				name = nt.FieldNames[i]
			}
			out[name] = item.Native()
		}
		return out
	default:
		return v.String()
	}
}

func nativeItems(items []Value) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item.Native()
	}
	return out
}

func nativeKey(k Value) string {
	if k.Kind() == ValueKindString {
		return k.data.(string)
	}
	return k.String()
}
