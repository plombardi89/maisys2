package config

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// bareKey matches the keys TOML allows without quotes.
var bareKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// tomlKey writes one part of a key, quoting it unless it's a bare key.
func tomlKey(key string) string {
	if bareKey.MatchString(key) {
		return key
	}
	return tomlString(key)
}

// tomlHeader writes a table header's key path.
func tomlHeader(path []string) string {
	parts := make([]string, len(path))
	for i, p := range path {
		parts[i] = tomlKey(p)
	}
	return "[" + strings.Join(parts, ".") + "]"
}

// tomlString writes s as a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\b':
			b.WriteString(`\b`)
		case '\t':
			b.WriteString(`\t`)
		case '\n':
			b.WriteString(`\n`)
		case '\f':
			b.WriteString(`\f`)
		case '\r':
			b.WriteString(`\r`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04X`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

// tomlValue writes v as a TOML value. Lists are written as inline arrays,
// and tables inside them as inline tables.
func tomlValue(v reflect.Value) string {
	switch v.Kind() {
	case reflect.String:
		return tomlString(v.String())
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(v.Uint(), 10)
	case reflect.Float32, reflect.Float64:
		return tomlFloat(v.Float(), v.Type().Bits())
	case reflect.Slice, reflect.Array:
		items := make([]string, v.Len())
		for i := range v.Len() {
			items[i] = tomlValue(v.Index(i))
		}
		return "[" + strings.Join(items, ", ") + "]"
	case reflect.Struct:
		var items []string
		for _, k := range configKeys(v) {
			items = append(items, tomlKey(k.name)+" = "+tomlValue(k.value))
		}
		return inlineTable(items)
	case reflect.Map:
		keys := make([]string, 0, v.Len())
		values := map[string]reflect.Value{}
		for it := v.MapRange(); it.Next(); {
			keys = append(keys, it.Key().String())
			values[it.Key().String()] = it.Value()
		}
		slices.Sort(keys)
		items := make([]string, len(keys))
		for i, k := range keys {
			items[i] = tomlKey(k) + " = " + tomlValue(values[k])
		}
		return inlineTable(items)
	default:
		// Config types can't hold other kinds (see checkStruct).
		return tomlString(fmt.Sprint(v))
	}
}

func inlineTable(items []string) string {
	if len(items) == 0 {
		return "{}"
	}
	return "{ " + strings.Join(items, ", ") + " }"
}

func tomlFloat(f float64, bits int) string {
	switch {
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	case math.IsNaN(f):
		return "nan"
	}
	s := strconv.FormatFloat(f, 'g', -1, bits)
	// Without a fraction or exponent, TOML would read an integer.
	if !strings.ContainsAny(s, ".e") {
		s += ".0"
	}
	return s
}
