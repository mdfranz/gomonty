package remote

import (
	"fmt"
	"math/big"
	"sort"

	monty "github.com/mdfranz/gomonty"
	montyv1 "github.com/mdfranz/gomonty/remote/internal/montyv1"
)

// Python values cross the wire as one node arena per message: containers hold
// the indexes of their children and nodes come in post-order, so every child
// index is lower than its holder's.
//
// Supported kinds are None, Ellipsis, NotImplemented, bool, int (including
// ints wider than 64 bits), float, complex, str, bytes, list, tuple, set,
// frozenset, dict, Decimal, Path and simple exception values. A result the
// worker sends in any other form (dates, class instances, functions) is
// reported as an error and not guessed at. Cycles and reprs, which the worker
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
		dict := v.Raw().(monty.Dict)
		pairs := make([]*montyv1.NodePair, len(dict))
		for i, pair := range dict {
			key, err := b.encode(pair.Key)
			if err != nil {
				return 0, err
			}
			value, err := b.encode(pair.Value)
			if err != nil {
				return 0, err
			}
			pairs[i] = &montyv1.NodePair{Key: key, Value: value}
		}
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Dict{Dict: &montyv1.NodePairs{Pairs: pairs}}}), nil
	case "decimal":
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Decimal{Decimal: string(v.Raw().(monty.Decimal))}}), nil
	case "path":
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Path{Path: string(v.Raw().(monty.Path))}}), nil
	case "exception":
		e := v.Raw().(monty.Exception)
		return b.add(&montyv1.MontyNode{Kind: &montyv1.MontyNode_Exception{Exception: &montyv1.Exception{ExcType: e.Type, Arg: e.Arg}}}), nil
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

// decodeValue builds the value at index root of an arena.
func decodeValue(arena *montyv1.Arena, root uint32) (monty.Value, error) {
	nodes := arena.GetNodes()
	if int(root) >= len(nodes) {
		return monty.Value{}, fmt.Errorf("remote: result index %d is outside an arena of %d nodes", root, len(nodes))
	}
	values := make([]monty.Value, len(nodes))
	for i, node := range nodes {
		v, err := decodeNode(node, values[:i])
		if err != nil {
			return monty.Value{}, fmt.Errorf("remote: node %d: %w", i, err)
		}
		values[i] = v
	}
	return values[root], nil
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
		pairs := k.Dict.GetPairs()
		dict := make(monty.Dict, 0, len(pairs))
		for _, p := range pairs {
			key, err := child(done, p.GetKey())
			if err != nil {
				return monty.Value{}, err
			}
			value, err := child(done, p.GetValue())
			if err != nil {
				return monty.Value{}, err
			}
			dict = append(dict, monty.Pair{Key: key, Value: value})
		}
		return monty.DictValue(dict), nil
	case *montyv1.MontyNode_Decimal:
		return monty.DecimalValue(monty.Decimal(k.Decimal)), nil
	case *montyv1.MontyNode_Path:
		return monty.PathValue(monty.Path(k.Path)), nil
	case *montyv1.MontyNode_Exception:
		return monty.ExceptionValue(monty.Exception{Type: k.Exception.GetExcType(), Arg: k.Exception.Arg}), nil
	case *montyv1.MontyNode_Repr:
		return monty.ReprValue(k.Repr), nil
	case *montyv1.MontyNode_Cycle:
		return monty.CycleValue(k.Cycle), nil
	default:
		return monty.Value{}, fmt.Errorf("a %T value is not supported by the remote backend yet", k)
	}
}
