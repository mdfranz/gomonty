package remote

import (
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strings"

	monty "github.com/mdfranz/gomonty"
	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
)

// Python values cross the wire as one node arena per message: containers hold
// the indexes of their children and nodes come in post-order, so every child
// index is lower than its holder's.
//
// Supported kinds are None, Ellipsis, NotImplemented, bool, int (including
// ints wider than 64 bits), float, complex, str, bytes, list, tuple, set,
// frozenset, dict, Decimal, Path, host function, simple exception, host module,
// named tuple, date, time, datetime, timedelta and timezone values. A worker
// may also send an open file handle. A result the worker sends in any other
// form (class instances, functions) is reported as an error and not guessed at. Cycles and reprs, which the worker
// only ever sends, decode to monty.CycleValue and monty.ReprValue.

type arenaBuilder struct {
	nodes []*montyv1.MontyNode
}

func (b *arenaBuilder) add(node *montyv1.MontyNode) uint32 {
	b.nodes = append(b.nodes, node)
	return uint32(len(b.nodes) - 1)
}

func (b *arenaBuilder) arena() *montyv1.Arena {
	return &montyv1.Arena{NodeCount: uint32(len(b.nodes)), Nodes: b.nodes}
}

func (b *arenaBuilder) items(values []monty.Value) (*montyv1.Indexes, error) {
	indexes := make([]uint32, len(values))
	for i, v := range values {
		idx, err := b.encode(v)
		if err != nil {
			return nil, err
		}
		indexes[i] = idx
	}
	return &montyv1.Indexes{Items: indexes}, nil
}

func (b *arenaBuilder) pairs(dict monty.Dict) (*montyv1.NodePairs, error) {
	pairs := make([]*montyv1.NodePair, len(dict))
	for i, pair := range dict {
		key, err := b.encode(pair.Key)
		if err != nil {
			return nil, err
		}
		value, err := b.encode(pair.Value)
		if err != nil {
			return nil, err
		}
		pairs[i] = &montyv1.NodePair{Key: key, Value: value}
	}
	return &montyv1.NodePairs{Pairs: pairs}, nil
}

// uuidBytes turns a canonical uuid string into its 16 raw bytes.
func uuidBytes(id string) ([]byte, error) {
	raw, err := hex.DecodeString(strings.ReplaceAll(id, "-", ""))
	if err != nil || len(raw) != 16 {
		return nil, fmt.Errorf("%q is not a uuid", id)
	}
	return raw, nil
}

func (b *arenaBuilder) encode(v monty.Value) (uint32, error) {
	switch kind := string(v.Kind()); kind {
	case "none":
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_None{None: &montyv1.Unit{}}}), nil
	case "ellipsis":
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Ellipsis{Ellipsis: &montyv1.Unit{}}}), nil
	case "not_implemented":
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_NotImplemented{NotImplemented: &montyv1.Unit{}}}), nil
	case "bool":
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Boolean{Boolean: v.Raw().(bool)}}), nil
	case "int":
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Int{Int: v.Raw().(int64)}}), nil
	case "big_int":
		n := v.Raw().(*big.Int)
		magnitude := new(big.Int).Abs(n).Bytes()
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Bigint{Bigint: &montyv1.BigInt{Negative: n.Sign() < 0, Magnitude: magnitude}}}), nil
	case "float":
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Float{Float: v.Raw().(float64)}}), nil
	case "complex":
		c := v.Raw().(complex128)
		re, im := real(c), imag(c)
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Complex{Complex: &montyv1.Complex{Real: &re, Imag: &im}}}), nil
	case "string":
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Str{Str: v.Raw().(string)}}), nil
	case "bytes":
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Bytes{Bytes: append([]byte(nil), v.Raw().([]byte)...)}}), nil
	case "list":
		items, err := b.items(v.Raw().([]monty.Value))
		if err != nil {
			return 0, err
		}
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_List{List: items}}), nil
	case "tuple":
		items, err := b.items([]monty.Value(v.Raw().(monty.Tuple)))
		if err != nil {
			return 0, err
		}
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Tuple{Tuple: items}}), nil
	case "set":
		items, err := b.items([]monty.Value(v.Raw().(monty.Set)))
		if err != nil {
			return 0, err
		}
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Set{Set: items}}), nil
	case "frozen_set":
		items, err := b.items([]monty.Value(v.Raw().(monty.FrozenSet)))
		if err != nil {
			return 0, err
		}
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_FrozenSet{FrozenSet: items}}), nil
	case "dict":
		pairs, err := b.pairs(v.Raw().(monty.Dict))
		if err != nil {
			return 0, err
		}
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Dict{Dict: pairs}}), nil
	case "decimal":
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Decimal{Decimal: string(v.Raw().(monty.Decimal))}}), nil
	case "path":
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Path{Path: string(v.Raw().(monty.Path))}}), nil
	case "function":
		f := v.Raw().(monty.Function)
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Function{Function: &montyv1.Function{Name: f.Name, Docstring: f.Docstring}}}), nil
	case "exception":
		e := v.Raw().(monty.Exception)
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Exception{Exception: &montyv1.Exception{ExcType: e.Type, Arg: e.Arg}}}), nil
	case "module":
		m := v.Raw().(monty.Module)
		id, err := uuidBytes(m.ID)
		if err != nil {
			return 0, fmt.Errorf("module %q: %w", m.Name, err)
		}
		attrs, err := b.pairs(m.Attrs)
		if err != nil {
			return 0, err
		}
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Module{Module: &montyv1.ModuleNode{Name: m.Name, Id: &montyv1.Uuid{Data: id}, Attrs: attrs}}}), nil
	case "named_tuple":
		nt := v.Raw().(monty.NamedTuple)
		values, err := b.items(nt.Values)
		if err != nil {
			return 0, err
		}
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_NamedTuple{NamedTuple: &montyv1.NamedTupleNode{TypeName: nt.TypeName, FieldNames: append([]string(nil), nt.FieldNames...), Values: values.Items}}}), nil
	case "date":
		d := v.Raw().(monty.Date)
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Date{Date: &montyv1.Date{Year: d.Year, Month: uint32(d.Month), Day: uint32(d.Day)}}}), nil
	case "time":
		t := v.Raw().(monty.Time)
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Time{Time: &montyv1.Time{
			Hour: uint32(t.Hour), Minute: uint32(t.Minute), Second: uint32(t.Second), Microsecond: t.Microsecond,
			OffsetSeconds: t.OffsetSeconds, TimezoneName: t.TimezoneName, Fold: uint32(t.Fold)}}}), nil
	case "datetime":
		d := v.Raw().(monty.DateTime)
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Datetime{Datetime: &montyv1.DateTime{
			Year: d.Year, Month: uint32(d.Month), Day: uint32(d.Day),
			Hour: uint32(d.Hour), Minute: uint32(d.Minute), Second: uint32(d.Second), Microsecond: d.Microsecond,
			OffsetSeconds: d.OffsetSeconds, TimezoneName: d.TimezoneName}}}), nil
	case "timedelta":
		d := v.Raw().(monty.TimeDelta)
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Timedelta{Timedelta: &montyv1.TimeDelta{Days: d.Days, Seconds: d.Seconds, Microseconds: d.Microseconds}}}), nil
	case "timezone":
		z := v.Raw().(monty.TimeZone)
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Timezone{Timezone: &montyv1.TimeZone{OffsetSeconds: z.OffsetSeconds, Name: z.Name}}}), nil
	default:
		return 0, fmt.Errorf("remote: a %s value cannot be sent to a remote worker yet", kind)
	}
}

// encodeInputs builds the named inputs of a feed over one shared arena.
// Names are sorted so the encoding is deterministic.
func encodeInputs(inputs map[string]monty.Value) ([]*montyv1.NamedRef, *montyv1.Arena, error) {
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)

	b := &arenaBuilder{}
	refs := make([]*montyv1.NamedRef, 0, len(names))
	for _, name := range names {
		idx, err := b.encode(inputs[name])
		if err != nil {
			return nil, nil, fmt.Errorf("input %q: %w", name, err)
		}
		refs = append(refs, &montyv1.NamedRef{Name: name, Value: idx})
	}
	return refs, b.arena(), nil
}

// decodeArena builds every value in an arena in one forward pass.
func decodeArena(arena *montyv1.Arena) ([]monty.Value, error) {
	nodes := arena.GetNodes()
	values := make([]monty.Value, len(nodes))
	for i, node := range nodes {
		v, err := decodeNode(node, values[:i])
		if err != nil {
			return nil, fmt.Errorf("remote: node %d: %w", i, err)
		}
		values[i] = v
	}
	return values, nil
}

// decodeValue builds the value at index root of an arena.
func decodeValue(arena *montyv1.Arena, root uint32) (monty.Value, error) {
	values, err := decodeArena(arena)
	if err != nil {
		return monty.Value{}, err
	}
	return pick(values, root)
}

// pick returns the value at idx of an already decoded arena.
func pick(values []monty.Value, idx uint32) (monty.Value, error) {
	if int(idx) >= len(values) {
		return monty.Value{}, fmt.Errorf("remote: index %d is outside an arena of %d nodes", idx, len(values))
	}
	return values[idx], nil
}

// child returns the already-decoded node at idx. Post-order means a child is
// always earlier than its holder; anything else is a malformed arena.
func child(done []monty.Value, idx uint32) (monty.Value, error) {
	if int(idx) >= len(done) {
		return monty.Value{}, fmt.Errorf("refers to node %d, which does not precede it", idx)
	}
	return done[idx], nil
}

func children(done []monty.Value, idx *montyv1.Indexes) ([]monty.Value, error) {
	out := make([]monty.Value, 0, len(idx.GetItems()))
	for _, i := range idx.GetItems() {
		v, err := child(done, i)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func decodePairs(done []monty.Value, np *montyv1.NodePairs) (monty.Dict, error) {
	pairs := np.GetPairs()
	dict := make(monty.Dict, 0, len(pairs))
	for _, p := range pairs {
		key, err := child(done, p.GetKey())
		if err != nil {
			return nil, err
		}
		value, err := child(done, p.GetValue())
		if err != nil {
			return nil, err
		}
		dict = append(dict, monty.Pair{Key: key, Value: value})
	}
	return dict, nil
}

func decodeNode(node *montyv1.MontyNode, done []monty.Value) (monty.Value, error) {
	switch k := node.GetKind().(type) {
	case *montyv1.MontyNode_None:
		return monty.None(), nil
	case *montyv1.MontyNode_Ellipsis:
		return monty.Ellipsis(), nil
	case *montyv1.MontyNode_NotImplemented:
		return monty.NotImplemented(), nil
	case *montyv1.MontyNode_Boolean:
		return monty.Bool(k.Boolean), nil
	case *montyv1.MontyNode_Int:
		return monty.Int(k.Int), nil
	case *montyv1.MontyNode_Bigint:
		n := new(big.Int).SetBytes(k.Bigint.GetMagnitude())
		if k.Bigint.GetNegative() {
			n.Neg(n)
		}
		return monty.BigInt(n), nil
	case *montyv1.MontyNode_Float:
		return monty.Float(k.Float), nil
	case *montyv1.MontyNode_Complex:
		return monty.Complex(complex(k.Complex.GetReal(), k.Complex.GetImag())), nil
	case *montyv1.MontyNode_Str:
		return monty.String(k.Str), nil
	case *montyv1.MontyNode_Bytes:
		return monty.Bytes(append([]byte(nil), k.Bytes...)), nil
	case *montyv1.MontyNode_List:
		items, err := children(done, k.List)
		if err != nil {
			return monty.Value{}, err
		}
		return monty.List(items...), nil
	case *montyv1.MontyNode_Tuple:
		items, err := children(done, k.Tuple)
		if err != nil {
			return monty.Value{}, err
		}
		return monty.TupleValue(items...), nil
	case *montyv1.MontyNode_Set:
		items, err := children(done, k.Set)
		if err != nil {
			return monty.Value{}, err
		}
		return monty.SetValue(monty.Set(items)), nil
	case *montyv1.MontyNode_FrozenSet:
		items, err := children(done, k.FrozenSet)
		if err != nil {
			return monty.Value{}, err
		}
		return monty.FrozenSetValue(monty.FrozenSet(items)), nil
	case *montyv1.MontyNode_Dict:
		dict, err := decodePairs(done, k.Dict)
		if err != nil {
			return monty.Value{}, err
		}
		return monty.DictValue(dict), nil
	case *montyv1.MontyNode_Decimal:
		return monty.DecimalValue(monty.Decimal(k.Decimal)), nil
	case *montyv1.MontyNode_Path:
		return monty.PathValue(monty.Path(k.Path)), nil
	case *montyv1.MontyNode_Exception:
		return monty.ExceptionValue(monty.Exception{Type: k.Exception.GetExcType(), Arg: k.Exception.Arg}), nil
	case *montyv1.MontyNode_Function:
		return monty.FunctionValue(monty.Function{Name: k.Function.GetName(), Docstring: k.Function.Docstring}), nil
	case *montyv1.MontyNode_Module:
		raw := k.Module.GetId().GetData()
		if len(raw) != 16 {
			return monty.Value{}, fmt.Errorf("module id is %d bytes, want 16", len(raw))
		}
		id := uuidString(raw)
		attrs, err := decodePairs(done, k.Module.GetAttrs())
		if err != nil {
			return monty.Value{}, err
		}
		return monty.ModuleValue(monty.Module{Name: k.Module.GetName(), ID: id, Attrs: attrs}), nil
	case *montyv1.MontyNode_NamedTuple:
		nt := k.NamedTuple
		values, err := children(done, &montyv1.Indexes{Items: nt.GetValues()})
		if err != nil {
			return monty.Value{}, err
		}
		return monty.NamedTupleValue(monty.NamedTuple{TypeName: nt.GetTypeName(), FieldNames: append([]string(nil), nt.GetFieldNames()...), Values: values}), nil
	case *montyv1.MontyNode_Date:
		d := k.Date
		return monty.DateValue(monty.Date{Year: d.GetYear(), Month: uint8(d.GetMonth()), Day: uint8(d.GetDay())}), nil
	case *montyv1.MontyNode_Time:
		t := k.Time
		return monty.TimeValue(monty.Time{
			Hour: uint8(t.GetHour()), Minute: uint8(t.GetMinute()), Second: uint8(t.GetSecond()), Microsecond: t.GetMicrosecond(),
			OffsetSeconds: t.OffsetSeconds, TimezoneName: t.TimezoneName, Fold: uint8(t.GetFold())}), nil
	case *montyv1.MontyNode_Datetime:
		d := k.Datetime
		return monty.DateTimeValue(monty.DateTime{
			Year: d.GetYear(), Month: uint8(d.GetMonth()), Day: uint8(d.GetDay()),
			Hour: uint8(d.GetHour()), Minute: uint8(d.GetMinute()), Second: uint8(d.GetSecond()), Microsecond: d.GetMicrosecond(),
			OffsetSeconds: d.OffsetSeconds, TimezoneName: d.TimezoneName}), nil
	case *montyv1.MontyNode_Timedelta:
		d := k.Timedelta
		return monty.TimeDeltaValue(monty.TimeDelta{Days: d.GetDays(), Seconds: d.GetSeconds(), Microseconds: d.GetMicroseconds()}), nil
	case *montyv1.MontyNode_Timezone:
		return monty.TimeZoneValue(monty.TimeZone{OffsetSeconds: k.Timezone.GetOffsetSeconds(), Name: k.Timezone.Name}), nil
	case *montyv1.MontyNode_FileHandle:
		return monty.FileHandleValue(monty.FileHandle{Path: k.FileHandle.GetPath(), Mode: k.FileHandle.GetMode(), Position: k.FileHandle.GetPosition()}), nil
	case *montyv1.MontyNode_Repr:
		return monty.ReprValue(k.Repr), nil
	case *montyv1.MontyNode_Cycle:
		return monty.CycleValue(k.Cycle), nil
	default:
		return monty.Value{}, fmt.Errorf("a %T value is not supported by the remote backend yet", k)
	}
}
