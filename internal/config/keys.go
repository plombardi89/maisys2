package config

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/pelletier/go-toml/v2/unstable"
)

// unknownKey is a key a file sets that its schema has no field for.
type unknownKey struct {
	path []string
	// suggest is the right spelling, when the key differs from a field's
	// only in case.
	suggest string
	// machineOnly is set for a machine file's own key in a profile.
	machineOnly bool
}

// unknownKeys compares the keys a file sets (present) with the fields of
// typ, by exact name. go-toml matches names without regard to case, so it
// decodes "Version" into version, but merging only sees exact names.
func unknownKeys(typ reflect.Type, present map[string]any, prefix []string) []unknownKey {
	typ = deref(typ)
	var unknown []unknownKey
	for _, k := range slices.Sorted(maps.Keys(present)) {
		path := append(slices.Clone(prefix), k)
		f, ok := fieldByKey(typ, k)
		if !ok {
			u := unknownKey{path: path}
			for _, name := range keyNames(typ) {
				if strings.EqualFold(name, k) {
					u.suggest = name
				}
			}
			unknown = append(unknown, u)
			continue
		}
		unknown = append(unknown, unknownNested(f.Type, present[k], path)...)
	}
	return unknown
}

// unknownNested checks the keys inside value, which a field of type t
// holds.
func unknownNested(t reflect.Type, value any, path []string) []unknownKey {
	t = deref(t)
	switch t.Kind() {
	case reflect.Struct:
		if table, ok := value.(map[string]any); ok {
			return unknownKeys(t, table, path)
		}
	case reflect.Map:
		table, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		var unknown []unknownKey
		for _, k := range slices.Sorted(maps.Keys(table)) {
			unknown = append(unknown, unknownNested(t.Elem(), table[k], append(slices.Clone(path), k))...)
		}
		return unknown
	case reflect.Slice:
		items, ok := value.([]any)
		if !ok {
			return nil
		}
		var unknown []unknownKey
		for _, item := range items {
			unknown = append(unknown, unknownNested(t.Elem(), item, path)...)
		}
		return unknown
	default:
	}
	return nil
}

// prune removes the key at path from the table present. Paths run through
// tables, tables keyed by name and lists of tables, but have no indexes
// for lists: the key is removed from every table in the list.
func prune(present any, path []string) {
	switch v := present.(type) {
	case map[string]any:
		if len(path) == 1 {
			delete(v, path[0])
			return
		}
		prune(v[path[0]], path[1:])
	case []any:
		for _, item := range v {
			prune(item, path)
		}
	default:
	}
}

// unknownKeyErrors describes unknown keys as errors that start with
// file:line:column, in the order they appear in the file. Only those that
// aren't a machine file's own keys wrap ErrUnknownKey.
func unknownKeyErrors(file string, data []byte, unknown []unknownKey) error {
	positions := keyPositions(data)
	type located struct {
		pos unstable.Position
		err error
	}
	var errs []located
	for _, u := range unknown {
		pos := positions[strings.Join(u.path, "\x00")]
		loc := file
		if pos.Line > 0 {
			loc = fmt.Sprintf("%s:%d:%d", file, pos.Line, pos.Column)
		}
		if u.machineOnly {
			errs = append(errs, located{pos, fmt.Errorf("%s: %s can only be set in a machine file", loc, displayKey(u.path))})
			continue
		}
		var hint string
		if u.suggest != "" {
			right := append(slices.Clone(u.path[:len(u.path)-1]), u.suggest)
			hint = fmt.Sprintf(" (keys are case-sensitive: did you mean %s?)", displayKey(right))
		}
		errs = append(errs, located{pos, fmt.Errorf("%s: %w %s%s", loc, ErrUnknownKey, displayKey(u.path), hint)})
	}
	slices.SortStableFunc(errs, func(a, b located) int {
		return cmp.Or(cmp.Compare(a.pos.Line, b.pos.Line), cmp.Compare(a.pos.Column, b.pos.Column))
	})
	out := make([]error, len(errs))
	for i, e := range errs {
		out[i] = e.err
	}
	return errors.Join(out...)
}

// keyPositions returns where each key in a TOML document first appears,
// keyed by the parts of its full path joined with "\x00". Every part of a
// dotted key or table header is recorded at its own position.
func keyPositions(data []byte) map[string]unstable.Position {
	positions := map[string]unstable.Position{}
	var p unstable.Parser
	p.Reset(data)

	// record adds the parts of node's key to prefix, recording the position
	// of each, and returns the full path.
	record := func(prefix []string, node *unstable.Node) []string {
		path := slices.Clone(prefix)
		it := node.Key()
		for it.Next() {
			part := it.Node()
			path = append(path, string(part.Data))
			key := strings.Join(path, "\x00")
			if _, ok := positions[key]; !ok {
				positions[key] = p.Shape(part.Raw).Start
			}
		}
		return path
	}
	var keyValue func(prefix []string, node *unstable.Node)
	keyValue = func(prefix []string, node *unstable.Node) {
		path := record(prefix, node)
		if v := node.Value(); v != nil && v.Kind == unstable.InlineTable {
			it := v.Children()
			for it.Next() {
				keyValue(path, it.Node())
			}
		}
	}

	var table []string
	for p.NextExpression() {
		e := p.Expression()
		switch e.Kind {
		case unstable.Table, unstable.ArrayTable:
			table = record(nil, e)
		case unstable.KeyValue:
			keyValue(table, e)
		default:
		}
	}
	return positions
}

// typeMismatch matches go-toml's message for a value of the wrong type.
var typeMismatch = regexp.MustCompile(`(?s)^cannot decode TOML (\w+) into `)

// decodeError turns a TOML error into errors that start with
// file:line:column. typ is the struct the file was decoded into. If the
// error comes from a copy of the file without the keys it ignores, locs is
// where the file itself sets each value, which the error points at
// instead.
func decodeError(file string, typ reflect.Type, err error, locs *locations) error {
	if de, ok := errors.AsType[*toml.DecodeError](err); ok {
		row, col := de.Position()
		at := fmt.Sprintf("%s:%d:%d", file, row, col)
		if locs != nil {
			at = locs.where(file, displayKey(de.Key()))
		}
		msg := strings.TrimPrefix(de.Error(), "toml: ")
		// go-toml describes type mismatches in Go terms; describe them in
		// config terms instead.
		if m := typeMismatch.FindStringSubmatch(msg); m != nil {
			if t := fieldTypeAt(typ, de.Key()); t != nil {
				msg = fmt.Sprintf("%s must be %s, not a TOML %s", displayKey(de.Key()), describeType(t), m[1])
			}
		}
		return fmt.Errorf("%s: %s", at, msg)
	}
	return fmt.Errorf("%s: %w", file, err)
}

// fieldTypeAt returns the type of the value at key in typ, following toml
// tags, embedded structs and tables keyed by name. It returns nil if key
// doesn't lead anywhere.
func fieldTypeAt(typ reflect.Type, key []string) reflect.Type {
	t := typ
	for _, k := range key {
		t = deref(t)
		switch t.Kind() {
		case reflect.Map:
			t = t.Elem()
		case reflect.Struct:
			f, ok := fieldByKey(t, k)
			if !ok {
				return nil
			}
			t = f.Type
		default:
			return nil
		}
	}
	return deref(t)
}

// describeType describes what a config value of type t looks like.
func describeType(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string in quotes"
	case reflect.Bool:
		return "true or false"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "a whole number"
	case reflect.Float32, reflect.Float64:
		return "a number"
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.String {
			return "a list of strings"
		}
		return "a list"
	case reflect.Map, reflect.Struct:
		return "a table"
	default:
		return "a " + t.String()
	}
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

// displayKey writes a key path the way it would appear in a TOML file.
func displayKey(parts []string) string {
	var path string
	for _, p := range parts {
		path = joinPath(path, p)
	}
	return path
}
