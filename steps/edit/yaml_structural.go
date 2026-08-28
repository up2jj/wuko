package edit

import (
	"bytes"
	"fmt"
	"strings"

	"github.com/theory/jsonpath/spec"
	"gopkg.in/yaml.v3"
)

type yamlStructure struct {
	data     []byte
	document yaml.Node
	// inserted counts the members already added at an offset. Those edits
	// coalesce into one, so each member after the first supplies the
	// separator an existing neighbour would otherwise have provided.
	inserted map[int]int
	flowAdds map[*yaml.Node]int
}

type yamlLocation struct {
	node, parent, key *yaml.Node
	index             int
	flow              bool
}

func patchYAMLMutations(data []byte, original, updated any, mutations []mutation) ([]byte, error) {
	structure := &yamlStructure{data: data, inserted: map[int]int{}, flowAdds: map[*yaml.Node]int{}}
	if err := yaml.NewDecoder(bytes.NewReader(data)).Decode(&structure.document); err != nil {
		return nil, err
	}
	edits := make([]textEdit, 0, len(mutations))
	for _, mutation := range mutations {
		if !mutation.changed {
			continue
		}
		var next []textEdit
		var err error
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
	return applyTextEdits(data, mergeDeletionEdits(coalesceIdenticalInsertions(edits)))
}

func (s *yamlStructure) locate(path spec.NormalizedPath) (yamlLocation, error) {
	node := &s.document
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return yamlLocation{}, fmt.Errorf("empty YAML document")
		}
		node = node.Content[0]
	}
	location := yamlLocation{node: node, index: -1}
	for _, selector := range path {
		if node.Style&yaml.FlowStyle != 0 {
			location.flow = true
		}
		location.parent = node
		switch selector := selector.(type) {
		case spec.Name:
			if node.Kind != yaml.MappingNode {
				return yamlLocation{}, fmt.Errorf("%s reaches non-mapping YAML node", path)
			}
			found := false
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value == string(selector) {
					location.key = node.Content[i]
					location.index = i / 2
					node = node.Content[i+1]
					found = true
					break
				}
			}
			if !found {
				return yamlLocation{}, fmt.Errorf("cannot locate %s in YAML source", path)
			}
		case spec.Index:
			if node.Kind != yaml.SequenceNode || int(selector) < 0 || int(selector) >= len(node.Content) {
				return yamlLocation{}, fmt.Errorf("cannot locate %s in YAML source", path)
			}
			location.key = nil
			location.index = int(selector)
			node = node.Content[int(selector)]
		}
		location.node = node
		if node.Alias != nil {
			return yamlLocation{}, fmt.Errorf("editing YAML aliases is not supported")
		}
	}
	return location, nil
}

func (s *yamlStructure) replaceEdit(path spec.NormalizedPath, value any) ([]textEdit, error) {
	location, err := s.locate(path)
	if err != nil {
		return nil, err
	}
	start, end, err := yamlNodeSpan(s.data, location.node, location.flow)
	if err != nil {
		return nil, err
	}
	text, err := renderYAML(value, location.node, location.node.Column-1, len(lineIndent(s.data, start)))
	if err != nil {
		return nil, err
	}
	return []textEdit{{start: start, end: end, text: text}}, nil
}

func (s *yamlStructure) createEdits(updated any, path spec.NormalizedPath) ([]textEdit, error) {
	parentPath := path[:len(path)-1]
	for {
		location, err := s.locate(parentPath)
		if err == nil && location.node.Kind == yaml.MappingNode {
			name := string(path[len(parentPath)].(spec.Name))
			value, err := valueAt(updated, path[:len(parentPath)+1])
			if err != nil {
				return nil, err
			}
			return s.addMappingMember(location.node, name, value)
		}
		if len(parentPath) == 0 {
			return nil, fmt.Errorf("cannot locate mapping parent for %s", path)
		}
		parentPath = parentPath[:len(parentPath)-1]
	}
}

func (s *yamlStructure) appendEdit(path spec.NormalizedPath, value any) ([]textEdit, error) {
	location, err := s.locate(path)
	if err != nil {
		return nil, err
	}
	if location.node.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("cannot locate sequence %s in YAML source", path)
	}
	return s.addSequenceElement(location.node, len(location.node.Content), value)
}

func (s *yamlStructure) insertEdit(path spec.NormalizedPath, position string, value any) ([]textEdit, error) {
	location, err := s.locate(path)
	if err != nil {
		return nil, err
	}
	if location.parent == nil || location.parent.Kind != yaml.SequenceNode {
		return nil, fmt.Errorf("cannot locate sequence parent for %s", path)
	}
	index := location.index
	if position == "after" {
		index++
	}
	return s.addSequenceElement(location.parent, index, value)
}

func (s *yamlStructure) deleteEdit(path spec.NormalizedPath) ([]textEdit, error) {
	location, err := s.locate(path)
	if err != nil {
		return nil, err
	}
	if location.parent.Style&yaml.FlowStyle != 0 {
		return s.deleteFlowEntry(location)
	}
	startNode := location.node
	if location.key != nil {
		startNode = location.key
	}
	lineStart, err := lineColumnOffset(s.data, startNode.Line, 1)
	if err != nil {
		return nil, err
	}
	keyStart, err := lineColumnOffset(s.data, startNode.Line, startNode.Column)
	if err != nil {
		return nil, err
	}
	_, end, err := yamlNodeSpan(s.data, location.node, false)
	if err != nil {
		return nil, err
	}
	// A key sharing its line with a sequence dash cannot take the line with it:
	// the "- " introduces the element, not this entry. Absorb the next
	// sibling's indentation instead so that key moves up onto the marker.
	if location.key != nil && len(bytes.TrimSpace(s.data[lineStart:keyStart])) > 0 {
		if next, ok := s.nextMappingKey(location); ok {
			return []textEdit{{start: keyStart, end: next}}, nil
		}
	}
	return []textEdit{{start: lineStart, end: throughLineEnd(s.data, end)}}, nil
}

// nextMappingKey returns the offset of the key that follows location in its
// mapping, which a deletion can reach back to for its own indentation.
func (s *yamlStructure) nextMappingKey(location yamlLocation) (int, bool) {
	parent := location.parent
	if parent == nil || parent.Kind != yaml.MappingNode {
		return 0, false
	}
	index := 2*location.index + 2
	if index >= len(parent.Content) {
		return 0, false
	}
	offset, err := lineColumnOffset(s.data, parent.Content[index].Line, parent.Content[index].Column)
	if err != nil {
		return 0, false
	}
	return offset, true
}

func (s *yamlStructure) renameEdit(path spec.NormalizedPath, name string) ([]textEdit, error) {
	location, err := s.locate(path)
	if err != nil {
		return nil, err
	}
	if location.key == nil {
		return nil, fmt.Errorf("cannot locate mapping key for %s", path)
	}
	start, err := lineColumnOffset(s.data, location.key.Line, location.key.Column)
	if err != nil {
		return nil, err
	}
	end := yamlKeyEnd(s.data, start, location.key, location.flow)
	text, err := renderYAML(name, location.key, location.key.Column-1, len(lineIndent(s.data, start)))
	if err != nil {
		return nil, err
	}
	return []textEdit{{start: start, end: end, text: text}}, nil
}

func (s *yamlStructure) mergeEdits(path spec.NormalizedPath, before, after any) ([]textEdit, error) {
	left := before.(map[string]any)
	right := after.(map[string]any)
	edits := make([]textEdit, 0)
	for _, key := range sortedKeys(right) {
		value := right[key]
		childPath := appendPath(path, spec.Name(key))
		previous, exists := left[key]
		if !exists {
			location, err := s.locate(path)
			if err != nil {
				return nil, err
			}
			added, err := s.addMappingMember(location.node, key, value)
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

func (s *yamlStructure) addMappingMember(mapping *yaml.Node, name string, value any) ([]textEdit, error) {
	keyText, err := encodeYAMLInline(name)
	if err != nil {
		return nil, err
	}
	valueText, err := encodeYAMLInline(value)
	if err != nil {
		return nil, err
	}
	if mapping.Style&yaml.FlowStyle != 0 {
		valueText, err = encodeYAMLFlow(value)
		if err != nil {
			return nil, err
		}
		_, end, err := yamlNodeSpan(s.data, mapping, true)
		if err != nil {
			return nil, err
		}
		member, err := s.flowSeparator(mapping, end-1)
		if err != nil {
			return nil, err
		}
		member = append(member, keyText...)
		member = append(member, []byte(": ")...)
		member = append(member, valueText...)
		return []textEdit{{start: end - 1, end: end - 1, text: member}}, nil
	}
	indent := mapping.Column - 1
	insert := 0
	var prefix []byte
	if len(mapping.Content) > 0 {
		last := mapping.Content[len(mapping.Content)-1]
		_, end, err := yamlNodeSpan(s.data, last, false)
		if err != nil {
			return nil, err
		}
		insert = throughLineEnd(s.data, end)
		prefix = s.lineBreakBefore(insert)
		indent = mapping.Content[len(mapping.Content)-2].Column - 1
	} else {
		start, _, err := yamlNodeSpan(s.data, mapping, false)
		if err != nil {
			return nil, err
		}
		insert = start
	}
	text := append(prefix, bytes.Repeat([]byte{' '}, indent)...)
	text = append(text, keyText...)
	if yamlCollection(value) {
		text = append(text, ':', '\n')
		text = append(text, indentBlock(valueText, indent+2)...)
	} else {
		text = append(text, []byte(": ")...)
		text = append(text, valueText...)
	}
	text = append(text, '\n')
	return []textEdit{{start: insert, end: insert, text: text}}, nil
}

func (s *yamlStructure) addSequenceElement(sequence *yaml.Node, index int, value any) ([]textEdit, error) {
	encoded, err := encodeYAMLInline(value)
	if err != nil {
		return nil, err
	}
	if sequence.Style&yaml.FlowStyle != 0 {
		_, end, err := yamlNodeSpan(s.data, sequence, true)
		if err != nil {
			return nil, err
		}
		if index >= len(sequence.Content) {
			prefix, err := s.flowSeparator(sequence, end-1)
			if err != nil {
				return nil, err
			}
			return []textEdit{{start: end - 1, end: end - 1, text: append(prefix, encoded...)}}, nil
		}
		start, _, err := yamlNodeSpan(s.data, sequence.Content[index], true)
		if err != nil {
			return nil, err
		}
		return []textEdit{{start: start, end: start, text: append(encoded, []byte(", ")...)}}, nil
	}
	indent := sequence.Column - 1
	insert := 0
	var prefix []byte
	switch {
	case len(sequence.Content) == 0:
		start, _, err := yamlNodeSpan(s.data, sequence, false)
		if err != nil {
			return nil, err
		}
		insert = start
	case index >= len(sequence.Content):
		_, end, err := yamlNodeSpan(s.data, sequence.Content[len(sequence.Content)-1], false)
		if err != nil {
			return nil, err
		}
		insert = throughLineEnd(s.data, end)
		prefix = s.lineBreakBefore(insert)
		if _, indent, err = s.entryMarker(sequence.Content[0]); err != nil {
			return nil, err
		}
	default:
		var err error
		if insert, indent, err = s.entryMarker(sequence.Content[index]); err != nil {
			return nil, err
		}
	}
	text := append(prefix, bytes.Repeat([]byte{' '}, max(indent, 0))...)
	text = append(text, []byte("- ")...)
	text = append(text, reindent(encoded, max(indent, 0)+2)...)
	text = append(text, '\n')
	return []textEdit{{start: insert, end: insert, text: text}}, nil
}

// flowSeparator returns the separator a new member of a flow collection needs.
// The collection may already end with a trailing comma, and members added by
// the same merge coalesce into a single edit at the closing bracket.
func (s *yamlStructure) flowSeparator(node *yaml.Node, closing int) ([]byte, error) {
	separator := []byte(", ")
	if s.flowAdds[node] == 0 {
		if len(node.Content) == 0 {
			separator = nil
		} else {
			_, end, err := yamlNodeSpan(s.data, node.Content[len(node.Content)-1], true)
			if err != nil {
				return nil, err
			}
			if bytes.ContainsRune(s.data[end:closing], ',') {
				separator = nil
			}
		}
	}
	s.flowAdds[node]++
	return separator, nil
}

// lineBreakBefore supplies the newline a source that does not end in one is
// missing before a member appended past its last line.
func (s *yamlStructure) lineBreakBefore(insert int) []byte {
	first := s.inserted[insert] == 0
	s.inserted[insert]++
	if first && insert > 0 && insert <= len(s.data) && s.data[insert-1] != '\n' {
		return []byte{'\n'}
	}
	return nil
}

// entryMarker returns the offset and indentation of the line carrying the "-"
// that introduces element, which need not be the line its content starts on.
func (s *yamlStructure) entryMarker(element *yaml.Node) (int, int, error) {
	offset, err := lineColumnOffset(s.data, element.Line, element.Column)
	if err != nil {
		return 0, 0, err
	}
	dash := offset - 1
	for dash >= 0 && (s.data[dash] == ' ' || s.data[dash] == '\t' || s.data[dash] == '\n' || s.data[dash] == '\r') {
		dash--
	}
	if dash < 0 || s.data[dash] != '-' {
		return 0, 0, fmt.Errorf("cannot locate the YAML sequence entry marker")
	}
	lineStart := dash
	for lineStart > 0 && s.data[lineStart-1] != '\n' {
		lineStart--
	}
	return lineStart, dash - lineStart, nil
}

func (s *yamlStructure) deleteFlowEntry(location yamlLocation) ([]textEdit, error) {
	entries := location.parent.Content
	stride := 1
	entryIndex := location.index
	startNode := location.node
	if location.parent.Kind == yaml.MappingNode {
		stride = 2
		entryIndex *= 2
		startNode = location.key
	}
	start, _, err := yamlNodeSpan(s.data, startNode, true)
	if err != nil {
		return nil, err
	}
	_, end, err := yamlNodeSpan(s.data, location.node, true)
	if err != nil {
		return nil, err
	}
	if len(entries) == stride {
		return []textEdit{{start: start, end: end}}, nil
	}
	if entryIndex+stride < len(entries) {
		nextStart, _, err := yamlNodeSpan(s.data, entries[entryIndex+stride], true)
		if err != nil {
			return nil, err
		}
		return []textEdit{{start: start, end: nextStart}}, nil
	}
	previous := entries[entryIndex-stride]
	_, previousEnd, err := yamlNodeSpan(s.data, previous, true)
	if err != nil {
		return nil, err
	}
	return []textEdit{{start: previousEnd, end: end}}, nil
}

func encodeYAMLInline(value any) ([]byte, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	_ = encoder.Close()
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func encodeYAMLFlow(value any) ([]byte, error) {
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return nil, err
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = *node.Content[0]
	}
	node.Style = yaml.FlowStyle
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	if err := encoder.Encode(&node); err != nil {
		return nil, err
	}
	_ = encoder.Close()
	return bytes.TrimSuffix(output.Bytes(), []byte{'\n'}), nil
}

func yamlCollection(value any) bool {
	switch value.(type) {
	case map[string]any, []any:
		return true
	default:
		return false
	}
}

func indentBlock(data []byte, indent int) []byte {
	prefix := []byte(strings.Repeat(" ", indent))
	lines := bytes.Split(data, []byte{'\n'})
	for i := range lines {
		if len(lines[i]) > 0 {
			lines[i] = append(append([]byte(nil), prefix...), lines[i]...)
		}
	}
	return bytes.Join(lines, []byte{'\n'})
}

func throughLineEnd(data []byte, offset int) int {
	if offset >= len(data) {
		return len(data)
	}
	if next := bytes.IndexByte(data[offset:], '\n'); next >= 0 {
		return offset + next + 1
	}
	return len(data)
}
