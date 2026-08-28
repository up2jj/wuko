package edit

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/theory/jsonpath/spec"
)

type jsonEntry struct {
	path                 spec.NormalizedPath
	keyStart, keyEnd     int
	valueStart, valueEnd int
}

type jsonContainer struct {
	kind        byte
	open, close int
	entries     []jsonEntry
	// added counts the members already appended to this container. They
	// coalesce into one edit, so each member after the first supplies the
	// separator an existing neighbour would otherwise have provided.
	added int
}

type jsonStructure struct {
	data       []byte
	pos        int
	spans      map[string]textEdit
	containers map[string]*jsonContainer
	// deletions counts the entries this batch removes from each container,
	// and collapsed records the containers already rewritten as empty.
	deletions map[string]int
	collapsed map[string]bool
}

func patchJSONMutations(data []byte, original, updated any, mutations []mutation) ([]byte, error) {
	structure := &jsonStructure{
		data: data, spans: make(map[string]textEdit), containers: make(map[string]*jsonContainer),
		deletions: make(map[string]int), collapsed: make(map[string]bool),
	}
	if err := structure.parse(); err != nil {
		return nil, err
	}
	for _, mutation := range mutations {
		if mutation.changed && mutation.operation == "delete" && len(mutation.match.Path) > 0 {
			structure.deletions[mutation.match.Path[:len(mutation.match.Path)-1].Pointer()]++
		}
	}
	edits := make([]textEdit, 0, len(mutations))
	for _, mutation := range mutations {
		if !mutation.changed {
			continue
		}
		var mutationEdits []textEdit
		var err error
		switch mutation.operation {
		case "set":
			if mutation.created {
				mutationEdits, err = structure.createEdits(updated, mutation.match.Path)
			} else {
				mutationEdits, err = structure.replaceEdit(mutation.match.Path, mutation.replacement)
			}
		case "delete":
			mutationEdits, err = structure.deleteEdit(mutation.match.Path)
		case "append":
			mutationEdits, err = structure.appendEdit(mutation.match.Path, mutation.replacement)
		case "insert":
			mutationEdits, err = structure.insertEdit(mutation.match.Path, mutation.position, mutation.replacement)
		case "merge":
			mutationEdits, err = structure.mergeEdits(mutation.match.Path, mutation.match.Node, mutation.replacement)
		case "rename":
			mutationEdits, err = structure.renameEdit(mutation.match.Path, mutation.name)
		}
		if err != nil {
			return nil, err
		}
		edits = append(edits, mutationEdits...)
	}
	return applyTextEdits(data, mergeDeletionEdits(coalesceIdenticalInsertions(edits)))
}

func (p *jsonStructure) parse() error {
	p.skipSpace()
	if err := p.value(nil); err != nil {
		return err
	}
	p.skipSpace()
	if p.pos != len(p.data) {
		return fmt.Errorf("unexpected JSON content at byte %d", p.pos+1)
	}
	return nil
}

func (p *jsonStructure) value(path spec.NormalizedPath) error {
	p.skipSpace()
	start := p.pos
	if p.pos >= len(p.data) {
		return fmt.Errorf("unexpected end of JSON")
	}
	switch p.data[p.pos] {
	case '{':
		container := &jsonContainer{kind: '{', open: p.pos}
		p.pos++
		p.skipSpace()
		if !p.take('}') {
			for {
				p.skipSpace()
				keyStart := p.pos
				raw, err := p.stringToken()
				if err != nil {
					return err
				}
				keyEnd := p.pos
				var key string
				if err := json.Unmarshal(raw, &key); err != nil {
					return err
				}
				p.skipSpace()
				if !p.take(':') {
					return fmt.Errorf("expected colon at byte %d", p.pos+1)
				}
				childPath := appendPath(path, spec.Name(key))
				p.skipSpace()
				valueStart := p.pos
				if err := p.value(childPath); err != nil {
					return err
				}
				container.entries = append(container.entries, jsonEntry{
					path: childPath, keyStart: keyStart, keyEnd: keyEnd, valueStart: valueStart, valueEnd: p.pos,
				})
				p.skipSpace()
				if p.take('}') {
					break
				}
				if !p.take(',') {
					return fmt.Errorf("expected comma at byte %d", p.pos+1)
				}
			}
		}
		container.close = p.pos - 1
		p.containers[path.Pointer()] = container
	case '[':
		container := &jsonContainer{kind: '[', open: p.pos}
		p.pos++
		p.skipSpace()
		if !p.take(']') {
			for index := 0; ; index++ {
				p.skipSpace()
				childPath := appendPath(path, spec.Index(index))
				valueStart := p.pos
				if err := p.value(childPath); err != nil {
					return err
				}
				container.entries = append(container.entries, jsonEntry{
					path: childPath, valueStart: valueStart, valueEnd: p.pos,
				})
				p.skipSpace()
				if p.take(']') {
					break
				}
				if !p.take(',') {
					return fmt.Errorf("expected comma at byte %d", p.pos+1)
				}
			}
		}
		container.close = p.pos - 1
		p.containers[path.Pointer()] = container
	case '"':
		if _, err := p.stringToken(); err != nil {
			return err
		}
	default:
		for p.pos < len(p.data) && !bytes.ContainsRune([]byte(" \t\r\n,]}"), rune(p.data[p.pos])) {
			p.pos++
		}
		if p.pos == start {
			return fmt.Errorf("invalid JSON value at byte %d", start+1)
		}
	}
	p.spans[path.Pointer()] = textEdit{start: start, end: p.pos}
	return nil
}

func (p *jsonStructure) stringToken() ([]byte, error) {
	start := p.pos
	if !p.take('"') {
		return nil, fmt.Errorf("expected string at byte %d", p.pos+1)
	}
	for p.pos < len(p.data) {
		switch p.data[p.pos] {
		case '\\':
			p.pos += 2
		case '"':
			p.pos++
			return p.data[start:p.pos], nil
		default:
			p.pos++
		}
	}
	return nil, fmt.Errorf("unterminated JSON string")
}

func (p *jsonStructure) skipSpace() {
	for p.pos < len(p.data) && bytes.ContainsRune([]byte(" \t\r\n"), rune(p.data[p.pos])) {
		p.pos++
	}
}

func (p *jsonStructure) take(want byte) bool {
	if p.pos < len(p.data) && p.data[p.pos] == want {
		p.pos++
		return true
	}
	return false
}

func (p *jsonStructure) replaceEdit(path spec.NormalizedPath, value any) ([]textEdit, error) {
	span, ok := p.spans[path.Pointer()]
	if !ok {
		return nil, fmt.Errorf("cannot locate %s in JSON source", path)
	}
	text, err := renderJSON(value, p.data[span.start:span.end], lineIndent(p.data, span.start))
	if err != nil {
		return nil, err
	}
	span.text = text
	return []textEdit{span}, nil
}

func (p *jsonStructure) createEdits(updated any, path spec.NormalizedPath) ([]textEdit, error) {
	parentPath := path[:len(path)-1]
	for {
		if container, ok := p.containers[parentPath.Pointer()]; ok && container.kind == '{' {
			child := path[len(parentPath)].(spec.Name)
			valuePath := path[:len(parentPath)+1]
			value, err := valueAt(updated, valuePath)
			if err != nil {
				return nil, err
			}
			return p.addObjectMember(container, string(child), value)
		}
		if len(parentPath) == 0 {
			return nil, fmt.Errorf("cannot locate object parent for %s", path)
		}
		parentPath = parentPath[:len(parentPath)-1]
	}
}

func (p *jsonStructure) appendEdit(path spec.NormalizedPath, value any) ([]textEdit, error) {
	container, ok := p.containers[path.Pointer()]
	if !ok || container.kind != '[' {
		return nil, fmt.Errorf("cannot locate array %s in JSON source", path)
	}
	text, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return []textEdit{p.addArrayElement(container, len(container.entries), text)}, nil
}

func (p *jsonStructure) insertEdit(path spec.NormalizedPath, position string, value any) ([]textEdit, error) {
	parent := path[:len(path)-1]
	container, ok := p.containers[parent.Pointer()]
	if !ok || container.kind != '[' {
		return nil, fmt.Errorf("cannot locate array parent for %s", path)
	}
	index := int(path[len(path)-1].(spec.Index))
	if position == "after" {
		index++
	}
	text, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return []textEdit{p.addArrayElement(container, index, text)}, nil
}

func (p *jsonStructure) deleteEdit(path spec.NormalizedPath) ([]textEdit, error) {
	parent := path[:len(path)-1]
	container, ok := p.containers[parent.Pointer()]
	if !ok {
		return nil, fmt.Errorf("cannot locate parent for %s in JSON source", path)
	}
	index := p.entryIndex(container, path)
	if index < 0 {
		return nil, fmt.Errorf("cannot locate %s in JSON source", path)
	}
	// Emptying a container leaves its layout behind: replace everything
	// between the brackets so no blank indented line survives.
	if p.deletions[parent.Pointer()] >= len(container.entries) {
		if p.collapsed[parent.Pointer()] {
			return nil, nil
		}
		p.collapsed[parent.Pointer()] = true
		return []textEdit{{start: container.open + 1, end: container.close}}, nil
	}
	entry := container.entries[index]
	if index < len(container.entries)-1 {
		next := container.entries[index+1]
		return []textEdit{{start: entryStart(entry), end: entryStart(next)}}, nil
	}
	previous := container.entries[index-1]
	return []textEdit{{start: previous.valueEnd, end: entry.valueEnd}}, nil
}

func (p *jsonStructure) renameEdit(path spec.NormalizedPath, name string) ([]textEdit, error) {
	container := p.containers[path[:len(path)-1].Pointer()]
	index := p.entryIndex(container, path)
	if index < 0 {
		return nil, fmt.Errorf("cannot locate %s in JSON source", path)
	}
	text, err := json.Marshal(name)
	if err != nil {
		return nil, err
	}
	entry := container.entries[index]
	return []textEdit{{start: entry.keyStart, end: entry.keyEnd, text: text}}, nil
}

func (p *jsonStructure) mergeEdits(path spec.NormalizedPath, before, after any) ([]textEdit, error) {
	left := before.(map[string]any)
	right := after.(map[string]any)
	edits := make([]textEdit, 0)
	for _, key := range sortedKeys(right) {
		value := right[key]
		childPath := appendPath(path, spec.Name(key))
		previous, exists := left[key]
		if !exists {
			container := p.containers[path.Pointer()]
			added, err := p.addObjectMember(container, key, value)
			if err != nil {
				return nil, err
			}
			edits = append(edits, added...)
			continue
		}
		if sameValue(previous, value) {
			continue
		}
		leftMap, leftOK := previous.(map[string]any)
		rightMap, rightOK := value.(map[string]any)
		if leftOK && rightOK {
			nested, err := p.mergeEdits(childPath, leftMap, rightMap)
			if err != nil {
				return nil, err
			}
			edits = append(edits, nested...)
			continue
		}
		replaced, err := p.replaceEdit(childPath, value)
		if err != nil {
			return nil, err
		}
		edits = append(edits, replaced...)
	}
	return edits, nil
}

func (p *jsonStructure) addObjectMember(container *jsonContainer, name string, value any) ([]textEdit, error) {
	key, _ := json.Marshal(name)
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	separator := []byte(": ")
	if len(container.entries) > 0 {
		entry := container.entries[0]
		between := p.data[entry.keyEnd:entry.valueStart]
		if bytes.Contains(between, []byte(":")) && !bytes.Contains(between, []byte(": ")) {
			separator = []byte(":")
		}
	}
	member := append(append(append([]byte(nil), key...), separator...), encoded...)
	return []textEdit{p.appendMember(container, member)}, nil
}

// appendMember places text after the last member of container, in the layout
// the container is already written in.
func (p *jsonStructure) appendMember(container *jsonContainer, text []byte) textEdit {
	multiline := bytes.Contains(p.data[container.open:container.close], []byte{'\n'})
	added := container.added
	container.added++
	if len(container.entries) == 0 {
		// An empty container already holds the whitespace its brackets sit
		// around, so the first member opens a line of its own after the
		// opening bracket rather than crowding the closing one.
		if multiline {
			prefix := []byte("\n" + lineIndent(p.data, container.close) + "  ")
			if added > 0 {
				prefix = append([]byte{','}, prefix...)
			}
			return textEdit{start: container.open + 1, end: container.open + 1, text: append(prefix, text...)}
		}
		var prefix []byte
		if added > 0 {
			prefix = []byte(", ")
		}
		return textEdit{start: container.close, end: container.close, text: append(prefix, text...)}
	}
	prefix := []byte(", ")
	if multiline {
		prefix = []byte(",\n" + lineIndent(p.data, entryStart(container.entries[0])))
	}
	last := container.entries[len(container.entries)-1]
	return textEdit{start: last.valueEnd, end: last.valueEnd, text: append(prefix, text...)}
}

func (p *jsonStructure) addArrayElement(container *jsonContainer, index int, value []byte) textEdit {
	if len(container.entries) == 0 || index >= len(container.entries) {
		return p.appendMember(container, value)
	}
	multiline := bytes.Contains(p.data[container.open:container.close], []byte{'\n'})
	indent := lineIndent(p.data, container.entries[0].valueStart)
	if index <= 0 {
		text := append(append([]byte(nil), value...), []byte(", ")...)
		if multiline {
			text = append(append([]byte(nil), value...), []byte(",\n"+indent)...)
		}
		return textEdit{start: container.entries[0].valueStart, end: container.entries[0].valueStart, text: text}
	}
	entry := container.entries[index]
	text := append(append([]byte(nil), value...), []byte(", ")...)
	if multiline {
		text = append(append([]byte(nil), value...), []byte(",\n"+indent)...)
	}
	return textEdit{start: entry.valueStart, end: entry.valueStart, text: text}
}

func (p *jsonStructure) entryIndex(container *jsonContainer, path spec.NormalizedPath) int {
	if container == nil {
		return -1
	}
	for i := range container.entries {
		if container.entries[i].path.Compare(path) == 0 {
			return i
		}
	}
	return -1
}

func entryStart(entry jsonEntry) int {
	if entry.keyStart != 0 {
		return entry.keyStart
	}
	return entry.valueStart
}

// Multiple merge keys append at the same object offset. Combine their text so
// applyTextEdits can continue to reject genuinely overlapping source edits.
func coalesceIdenticalInsertions(edits []textEdit) []textEdit {
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
