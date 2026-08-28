package edit

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/theory/jsonpath/spec"
)

var bareTOMLKey = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

type tomlEntry struct {
	path                 spec.NormalizedPath
	keyStart, keyEnd     int
	valueStart, valueEnd int
	lineStart, lineEnd   int
}

type tomlTable struct {
	insertAt int
}

type tomlStructure struct {
	data    []byte
	entries map[string]tomlEntry
	tables  map[string]tomlTable
	// inserted counts the values already written at an offset. Those edits
	// coalesce into one, so each value after the first supplies the
	// separator an existing neighbour would otherwise have provided.
	inserted map[int]int
}

func patchTOMLMutations(data []byte, original, updated any, mutations []mutation) ([]byte, error) {
	valueOnly := true
	matches := make([]*spec.LocatedNode, 0, len(mutations))
	replacements := make([]any, 0, len(mutations))
	for _, mutation := range mutations {
		if !mutation.changed {
			continue
		}
		if mutation.operation != "set" || mutation.created {
			valueOnly = false
			break
		}
		matches = append(matches, mutation.match)
		replacements = append(replacements, mutation.replacement)
	}
	if valueOnly {
		return patchDocument(data, "toml", matches, replacements)
	}
	structure, err := parseTOMLStructure(data)
	if err != nil {
		return nil, err
	}
	edits := make([]textEdit, 0, len(mutations))
	for _, mutation := range mutations {
		if !mutation.changed {
			continue
		}
		var next []textEdit
		switch mutation.operation {
		case "set":
			if mutation.created {
				next, err = structure.createEdits(updated, mutation.match.Path)
			} else {
				next, err = structure.replaceEdit(mutation.match.Path, mutation.replacement)
			}
		case "delete":
			next, err = structure.deleteEdit(mutation.match.Path)
		case "append":
			next, err = structure.appendEdit(mutation.match.Path, mutation.replacement)
		case "insert":
			next, err = structure.insertEdit(mutation.match.Path, mutation.position, mutation.replacement)
		case "merge":
			next, err = structure.mergeEdits(mutation.match.Path, mutation.match.Node, mutation.replacement)
		case "rename":
			next, err = structure.renameEdit(mutation.match.Path, mutation.name)
		}
		if err != nil {
			return nil, err
		}
		edits = append(edits, next...)
	}
	return applyTextEdits(data, mergeDeletionEdits(coalesceTOMLInsertions(edits)))
}

func parseTOMLStructure(data []byte) (*tomlStructure, error) {
	structure := &tomlStructure{
		data: data, entries: make(map[string]tomlEntry), tables: make(map[string]tomlTable),
		inserted: make(map[int]int),
	}
	current := spec.NormalizedPath(nil)
	structure.tables[""] = tomlTable{insertAt: len(data)}
	arrayNext := make(map[string]int)
	arrayCurrent := make(map[string]int)
	for offset := 0; offset < len(data); {
		lineEnd := bytes.IndexByte(data[offset:], '\n')
		if lineEnd < 0 {
			lineEnd = len(data) - offset
		}
		lineStop := offset + lineEnd
		line := data[offset:lineStop]
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' {
			offset = throughLineEnd(data, lineStop)
			continue
		}
		if trimmed[0] == '[' {
			previous := structure.tables[current.Pointer()]
			previous.insertAt = tomlCommentBlockStart(data, offset)
			structure.tables[current.Pointer()] = previous
			array := len(trimmed) > 1 && trimmed[1] == '['
			closing := []byte("]")
			nameStart := 1
			if array {
				closing = []byte("]]")
				nameStart = 2
			}
			end := tomlHeaderEnd(trimmed, closing)
			if end < 0 {
				return nil, fmt.Errorf("unterminated TOML table header")
			}
			parts, err := tomlKeyParts(string(bytes.TrimSpace(trimmed[nameStart:end])))
			if err != nil {
				return nil, err
			}
			var resolved []string
			if !array {
				resolved = expandArrayPath(parts, arrayCurrent)
			} else {
				resolved = append(expandArrayPath(parts[:len(parts)-1], arrayCurrent), parts[len(parts)-1])
				key := "/" + strings.Join(escapeParts(resolved), "/")
				index := arrayNext[key]
				arrayNext[key] = index + 1
				arrayCurrent[key] = index
				resolved = append(resolved, strconv.Itoa(index))
			}
			current = tomlPartsPath(resolved)
			next := throughLineEnd(data, lineStop)
			structure.tables[current.Pointer()] = tomlTable{insertAt: len(data)}
			offset = next
			continue
		}
		eq := tomlEquals(line)
		if eq < 0 {
			return nil, fmt.Errorf("cannot parse TOML assignment near byte %d", offset+1)
		}
		keys, err := tomlKeyParts(string(bytes.TrimSpace(line[:eq])))
		if err != nil {
			return nil, err
		}
		path := append(spec.NormalizedPath(nil), current...)
		path = append(path, namesPath(keys)...)
		valueStart := offset + eq + 1
		for valueStart < len(data) && (data[valueStart] == ' ' || data[valueStart] == '\t') {
			valueStart++
		}
		valueEnd := tomlValueEnd(data, valueStart)
		keyStart := offset
		for keyStart < offset+eq && (data[keyStart] == ' ' || data[keyStart] == '\t') {
			keyStart++
		}
		keyEnd := offset + eq
		for keyEnd > keyStart && (data[keyEnd-1] == ' ' || data[keyEnd-1] == '\t') {
			keyEnd--
		}
		structure.entries[path.Pointer()] = tomlEntry{
			path: path, keyStart: keyStart, keyEnd: keyEnd, valueStart: valueStart, valueEnd: valueEnd,
			lineStart: offset, lineEnd: throughLineEnd(data, valueEnd),
		}
		offset = throughLineEnd(data, valueEnd)
	}
	last := structure.tables[current.Pointer()]
	last.insertAt = len(data)
	structure.tables[current.Pointer()] = last
	return structure, nil
}

// tomlCommentBlockStart backs up over the comment lines directly above a table
// header so a created key lands before them, leaving the comment attached to
// the table it documents.
func tomlCommentBlockStart(data []byte, offset int) int {
	start := offset
	for start > 0 {
		lineStart := start - 1
		for lineStart > 0 && data[lineStart-1] != '\n' {
			lineStart--
		}
		if trimmed := bytes.TrimSpace(data[lineStart : start-1]); len(trimmed) == 0 || trimmed[0] != '#' {
			break
		}
		start = lineStart
	}
	return start
}

// inlineTable reports whether entry assigns an inline table, whose members this
// parser does not record and therefore cannot edit in place.
func (s *tomlStructure) inlineTable(entry tomlEntry) bool {
	start := entry.valueStart
	for start < entry.valueEnd && (s.data[start] == ' ' || s.data[start] == '\t') {
		start++
	}
	return start < entry.valueEnd && s.data[start] == '{'
}

// missing explains why path has no assignment of its own, naming the inline
// table that holds it rather than reporting it as unlocatable.
func (s *tomlStructure) missing(path spec.NormalizedPath) error {
	for parent := path[:len(path)-1]; len(parent) > 0; parent = parent[:len(parent)-1] {
		entry, ok := s.entries[parent.Pointer()]
		if !ok {
			continue
		}
		if s.inlineTable(entry) {
			return fmt.Errorf("editing %s inside a TOML inline table is not supported", path)
		}
		break
	}
	return fmt.Errorf("cannot safely locate %s in TOML source", path)
}

// arrayAppendEdit places encoded after the last element of an inline array. It
// writes the separator itself rather than relying on the closing bracket, so an
// array that already carries a trailing comma does not gain a second one.
func (s *tomlStructure) arrayAppendEdit(elements []textEdit, open, closing int, encoded []byte) textEdit {
	if len(elements) == 0 {
		var prefix []byte
		if s.inserted[closing] > 0 {
			prefix = []byte(", ")
		}
		s.inserted[closing]++
		return textEdit{start: closing, end: closing, text: append(prefix, encoded...)}
	}
	last := elements[len(elements)-1]
	prefix := []byte(", ")
	if bytes.ContainsRune(s.data[open:closing], '\n') {
		prefix = []byte(",\n" + lineIndent(s.data, last.start))
	}
	return textEdit{start: last.end, end: last.end, text: append(prefix, encoded...)}
}

func (s *tomlStructure) replaceEdit(path spec.NormalizedPath, value any) ([]textEdit, error) {
	entry, ok := s.entries[path.Pointer()]
	if !ok {
		return nil, s.missing(path)
	}
	text, err := renderTOML(value)
	if err != nil {
		return nil, err
	}
	return []textEdit{{start: entry.valueStart, end: entry.valueEnd, text: text}}, nil
}

func (s *tomlStructure) createEdits(updated any, path spec.NormalizedPath) ([]textEdit, error) {
	value, err := valueAt(updated, path)
	if err != nil {
		return nil, err
	}
	return s.addMissingValue(path, value)
}

func (s *tomlStructure) addMissingValue(path spec.NormalizedPath, value any) ([]textEdit, error) {
	if parent := path[:len(path)-1]; len(parent) > 0 {
		if entry, ok := s.entries[parent.Pointer()]; ok && s.inlineTable(entry) {
			return nil, s.missing(path)
		}
	}
	tablePath := path[:len(path)-1]
	for {
		if table, ok := s.tables[tablePath.Pointer()]; ok {
			relative := path[len(tablePath):]
			return s.addAssignment(table, relative, value)
		}
		if len(tablePath) == 0 {
			break
		}
		tablePath = tablePath[:len(tablePath)-1]
	}
	return nil, fmt.Errorf("cannot locate TOML table for %s", path)
}

func (s *tomlStructure) deleteEdit(path spec.NormalizedPath) ([]textEdit, error) {
	if _, ok := path[len(path)-1].(spec.Index); ok {
		return s.deleteArrayElement(path)
	}
	entry, ok := s.entries[path.Pointer()]
	if !ok {
		return nil, s.missing(path)
	}
	return []textEdit{{start: entry.lineStart, end: entry.lineEnd}}, nil
}

func (s *tomlStructure) renameEdit(path spec.NormalizedPath, name string) ([]textEdit, error) {
	entry, ok := s.entries[path.Pointer()]
	if !ok {
		return nil, s.missing(path)
	}
	keyText := s.data[entry.keyStart:entry.keyEnd]
	parts, err := tomlKeyParts(string(keyText))
	if err != nil {
		return nil, err
	}
	parts[len(parts)-1] = name
	encoded := make([]string, len(parts))
	for i, part := range parts {
		encoded[i] = encodeTOMLKey(part)
	}
	return []textEdit{{start: entry.keyStart, end: entry.keyEnd, text: []byte(strings.Join(encoded, "."))}}, nil
}

func (s *tomlStructure) appendEdit(path spec.NormalizedPath, value any) ([]textEdit, error) {
	entry, ok := s.entries[path.Pointer()]
	if !ok {
		return nil, fmt.Errorf("cannot safely locate array %s in TOML source", path)
	}
	encoded, err := renderTOML(value)
	if err != nil {
		return nil, err
	}
	elements, open, close, err := tomlArrayElements(s.data, entry)
	if err != nil {
		return nil, err
	}
	return []textEdit{s.arrayAppendEdit(elements, open, close, encoded)}, nil
}

func (s *tomlStructure) insertEdit(path spec.NormalizedPath, position string, value any) ([]textEdit, error) {
	parent := path[:len(path)-1]
	entry, ok := s.entries[parent.Pointer()]
	if !ok {
		return nil, fmt.Errorf("cannot safely locate array parent for %s", path)
	}
	elements, open, close, err := tomlArrayElements(s.data, entry)
	if err != nil {
		return nil, err
	}
	index := int(path[len(path)-1].(spec.Index))
	if position == "after" {
		index++
	}
	encoded, err := renderTOML(value)
	if err != nil {
		return nil, err
	}
	if index >= len(elements) {
		return []textEdit{s.arrayAppendEdit(elements, open, close, encoded)}, nil
	}
	return []textEdit{{start: elements[index].start, end: elements[index].start, text: append(encoded, []byte(", ")...)}}, nil
}

func (s *tomlStructure) deleteArrayElement(path spec.NormalizedPath) ([]textEdit, error) {
	parent := path[:len(path)-1]
	entry, ok := s.entries[parent.Pointer()]
	if !ok {
		return nil, fmt.Errorf("cannot safely locate array parent for %s", path)
	}
	elements, _, _, err := tomlArrayElements(s.data, entry)
	if err != nil {
		return nil, err
	}
	index := int(path[len(path)-1].(spec.Index))
	if index < 0 || index >= len(elements) {
		return nil, fmt.Errorf("array index %d does not exist", index)
	}
	if len(elements) == 1 {
		return []textEdit{{start: elements[0].start, end: elements[0].end}}, nil
	}
	if index < len(elements)-1 {
		return []textEdit{{start: elements[index].start, end: elements[index+1].start}}, nil
	}
	return []textEdit{{start: elements[index-1].end, end: elements[index].end}}, nil
}

func (s *tomlStructure) mergeEdits(path spec.NormalizedPath, before, after any) ([]textEdit, error) {
	left := before.(map[string]any)
	right := after.(map[string]any)
	edits := make([]textEdit, 0)
	for _, key := range sortedKeys(right) {
		value := right[key]
		childPath := appendPath(path, spec.Name(key))
		previous, exists := left[key]
		if !exists {
			created, err := s.addMissingValue(childPath, value)
			if err != nil {
				return nil, err
			}
			edits = append(edits, created...)
			continue
		}
		if sameValue(previous, value) {
			continue
		}
		leftMap, leftOK := previous.(map[string]any)
		rightMap, rightOK := value.(map[string]any)
		if leftOK && rightOK {
			nested, err := s.mergeEdits(childPath, leftMap, rightMap)
			if err != nil {
				return nil, err
			}
			edits = append(edits, nested...)
			continue
		}
		replaced, err := s.replaceEdit(childPath, value)
		if err != nil {
			return nil, err
		}
		edits = append(edits, replaced...)
	}
	return edits, nil
}

func (s *tomlStructure) addAssignment(table tomlTable, path spec.NormalizedPath, value any) ([]textEdit, error) {
	parts := make([]string, len(path))
	for i, selector := range path {
		name, ok := selector.(spec.Name)
		if !ok {
			return nil, fmt.Errorf("cannot create array index in TOML assignment")
		}
		parts[i] = encodeTOMLKey(string(name))
	}
	encoded, err := renderTOML(value)
	if err != nil {
		return nil, err
	}
	prefix := []byte{}
	if s.inserted[table.insertAt] == 0 && table.insertAt > 0 && s.data[table.insertAt-1] != '\n' {
		prefix = []byte{'\n'}
	}
	s.inserted[table.insertAt]++
	text := append(prefix, []byte(strings.Join(parts, ".")+" = ")...)
	text = append(text, encoded...)
	text = append(text, '\n')
	return []textEdit{{start: table.insertAt, end: table.insertAt, text: text}}, nil
}

func namesPath(parts []string) spec.NormalizedPath {
	path := make(spec.NormalizedPath, len(parts))
	for i, part := range parts {
		path[i] = spec.Name(part)
	}
	return path
}

func tomlPartsPath(parts []string) spec.NormalizedPath {
	path := make(spec.NormalizedPath, len(parts))
	for i, part := range parts {
		if index, err := strconv.Atoi(part); err == nil {
			path[i] = spec.Index(index)
		} else {
			path[i] = spec.Name(part)
		}
	}
	return path
}

func encodeTOMLKey(key string) string {
	if bareTOMLKey.MatchString(key) {
		return key
	}
	return strconv.Quote(key)
}

func tomlArrayElements(data []byte, entry tomlEntry) ([]textEdit, int, int, error) {
	start := entry.valueStart
	for start < entry.valueEnd && (data[start] == ' ' || data[start] == '\t') {
		start++
	}
	if start >= entry.valueEnd || data[start] != '[' {
		return nil, 0, 0, fmt.Errorf("selected TOML value is not an inline array")
	}
	close := balancedEnd(data, start, '[') - 1
	elements := []textEdit{}
	position := start + 1
	for position < close {
		for position < close && (data[position] == ' ' || data[position] == '\t' || data[position] == '\r' || data[position] == '\n') {
			position++
		}
		if position >= close {
			break
		}
		end := tomlArrayElementEnd(data, position, close)
		elements = append(elements, textEdit{start: position, end: end})
		position = end
		for position < close && data[position] != ',' {
			position++
		}
		if position < close {
			position++
		}
	}
	return elements, start, close, nil
}

func tomlArrayElementEnd(data []byte, start, close int) int {
	depth := 0
	quote := byte(0)
	for i := start; i < close; i++ {
		c := data[i]
		if quote != 0 {
			if quote == '"' && c == '\\' {
				i++
				continue
			}
			if c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			continue
		}
		switch c {
		case '[', '{':
			depth++
		case ']', '}':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				return trimSpaceEnd(data, start, i)
			}
		}
	}
	return trimSpaceEnd(data, start, close)
}

func coalesceTOMLInsertions(edits []textEdit) []textEdit {
	result := make([]textEdit, 0, len(edits))
	for _, edit := range edits {
		merged := false
		if edit.start == edit.end {
			for i := range result {
				if result[i].start == edit.start && result[i].end == edit.end {
					result[i].text = append(result[i].text, edit.text...)
					merged = true
					break
				}
			}
		}
		if !merged {
			result = append(result, edit)
		}
	}
	return result
}
