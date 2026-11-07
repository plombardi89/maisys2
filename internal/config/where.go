package config

import (
	"fmt"
	"path"
	"slices"

	"github.com/pelletier/go-toml/v2/unstable"
)

// locations records where a file sets each value. Paths are dotted, in
// the form Origins uses.
type locations struct {
	// values maps each key the file gives a value to where the value
	// starts.
	values map[string]unstable.Position
	// tables maps each table the file names, in a table header or as part
	// of a dotted key, to where it first names it. Inline tables are both
	// values and tables.
	tables map[string]unstable.Position
	// items maps each list to where each of its strings appears.
	items map[string]map[string]unstable.Position
}

// locate finds where data, a TOML document that decodes without errors,
// sets each value.
func locate(data []byte) *locations {
	loc := &locations{
		values: map[string]unstable.Position{},
		tables: map[string]unstable.Position{},
		items:  map[string]map[string]unstable.Position{},
	}
	var p unstable.Parser
	p.Reset(data)

	// key adds the parts of node's key to prefix, and returns the full path
	// and the offset just past the key. Every part of a table header names
	// a table, and so does every part of a dotted key but the last.
	key := func(prefix string, node *unstable.Node, header bool) (string, int) {
		var parts []*unstable.Node
		for it := node.Key(); it.Next(); {
			parts = append(parts, it.Node())
		}
		full, end := prefix, 0
		for i, part := range parts {
			full = joinPath(full, string(part.Data))
			end = int(part.Raw.Offset + part.Raw.Length)
			if _, seen := loc.tables[full]; !seen && (header || i < len(parts)-1) {
				loc.tables[full] = p.Shape(part.Raw).Start
			}
		}
		return full, end
	}

	var keyValue func(prefix string, node *unstable.Node)
	keyValue = func(prefix string, node *unstable.Node) {
		full, end := key(prefix, node, false)
		loc.values[full] = p.Shape(unstable.Range{Offset: uint32(valueOffset(data, end))}).Start
		v := node.Value()
		switch v.Kind {
		case unstable.InlineTable:
			loc.tables[full] = loc.values[full]
			it := v.Children()
			for it.Next() {
				keyValue(full, it.Node())
			}
		case unstable.Array:
			items := map[string]unstable.Position{}
			it := v.Children()
			for it.Next() {
				item := it.Node()
				if _, seen := items[string(item.Data)]; item.Kind == unstable.String && !seen {
					items[string(item.Data)] = p.Shape(item.Raw).Start
				}
			}
			loc.items[full] = items
		default:
		}
	}

	var table string
	for p.NextExpression() {
		e := p.Expression()
		switch e.Kind {
		case unstable.Table, unstable.ArrayTable:
			table, _ = key("", e, true)
		case unstable.KeyValue:
			keyValue(table, e)
		default:
		}
	}
	return loc
}

// valueOffset returns the offset of the value that follows a key ending at
// offset end: past the = and the spaces around it.
func valueOffset(data []byte, end int) int {
	i := end
	skipSpaces := func() {
		for i < len(data) && (data[i] == ' ' || data[i] == '\t') {
			i++
		}
	}
	skipSpaces()
	if i < len(data) && data[i] == '=' {
		i++
	}
	skipSpaces()
	return i
}

// JoinKey joins keys into a dotted path, the form Origins and Where use.
// Keys that contain a dot or a space are quoted, so JoinKey("files",
// "items", "a.b") is `files.items."a.b"`.
func JoinKey(keys ...string) string {
	return displayKey(keys)
}

// MachineFile returns the path of a machine's file in a config repo.
func MachineFile(name string) string {
	return path.Join(machinesDir, name+".toml")
}

func profileFile(name string) string {
	return path.Join(profilesDir, name+".toml")
}

// Where returns where the value at path (a dotted path, as in Origins) was
// last set, as "file:line:column", pointing at the value. For a table, it
// points at where the last file to name the table names it. It returns
// just the file if the position isn't known, and "" if no file sets the
// value, so it has its default.
func (m *Machine) Where(path string) string {
	if files := m.Origins[path]; len(files) > 0 {
		return m.whereIn(files[len(files)-1], path)
	}
	for _, file := range slices.Backward(m.layers) {
		if loc := m.locs[file]; loc != nil {
			if _, ok := loc.tables[path]; ok {
				return m.whereIn(file, path)
			}
		}
	}
	return ""
}

// WhereItem is like Where for one item of the list at path: it returns
// where the first file that added item to the list sets it.
func (m *Machine) WhereItem(path, item string) string {
	for _, file := range m.Origins[path] {
		if pos, ok := m.locs[file].item(path, item); ok {
			return position(file, pos)
		}
	}
	return m.Where(path)
}

// whereIn returns where file sets the value or table at path.
func (m *Machine) whereIn(file, path string) string {
	return m.locs[file].where(file, path)
}

// locate returns where to report a problem with the value at path: where
// it was set, or else where the closest table around it was, or else the
// machine file.
func (m *Machine) locate(path string) string {
	for p := path; p != ""; p = parentPath(p) {
		if w := m.Where(p); w != "" {
			return w
		}
	}
	return m.file()
}

func (m *Machine) file() string {
	return MachineFile(m.Name)
}

// where returns "file:line:column" for the value or table at path, or just
// file if loc doesn't have it.
func (loc *locations) where(file, path string) string {
	if loc != nil {
		if pos, ok := loc.values[path]; ok {
			return position(file, pos)
		}
		if pos, ok := loc.tables[path]; ok {
			return position(file, pos)
		}
	}
	return file
}

// whereItem is where for one item of the list at path.
func (loc *locations) whereItem(file, path, item string) string {
	if pos, ok := loc.item(path, item); ok {
		return position(file, pos)
	}
	return loc.where(file, path)
}

// item returns where item appears in the list at path.
func (loc *locations) item(path, item string) (unstable.Position, bool) {
	if loc == nil {
		return unstable.Position{}, false
	}
	pos, ok := loc.items[path][item]
	return pos, ok
}

func position(file string, pos unstable.Position) string {
	return fmt.Sprintf("%s:%d:%d", file, pos.Line, pos.Column)
}

// parentPath returns the path of the table that holds the value at path,
// or "" for a top-level key.
func parentPath(path string) string {
	quoted := false
	last := -1
	for i := range len(path) {
		switch path[i] {
		case '"':
			quoted = !quoted
		case '.':
			if !quoted {
				last = i
			}
		default:
		}
	}
	if last < 0 {
		return ""
	}
	return path[:last]
}
