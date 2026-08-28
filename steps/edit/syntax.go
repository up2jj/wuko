package edit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/theory/jsonpath/spec"
	"gopkg.in/yaml.v3"
)

// patchMutations applies semantic mutations through the syntax model for the
// source format, preserving bytes unrelated to the changed entries.
func patchMutations(data []byte, format string, original, updated any, mutations []mutation) ([]byte, error) {
	switch format {
	case "json":
		return patchJSONMutations(data, original, updated, mutations)
	case "yaml":
		return patchYAMLMutations(data, original, updated, mutations)
	case "toml":
		return patchTOMLMutations(data, original, updated, mutations)
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
}

type textEdit struct {
	start int
	end   int
	text  []byte
}

func patchDocument(data []byte, format string, matches []*spec.LocatedNode, replacements []any) ([]byte, error) {
	edits := make([]textEdit, len(matches))
	var err error
	switch format {
	case "json":
		spans := make(map[string]textEdit)
		parser := jsonSpanParser{data: data, spans: spans}
		if err := parser.parse(); err != nil {
			return nil, err
		}
		for i, match := range matches {
			span, ok := spans[match.Path.Pointer()]
			if !ok {
				return nil, fmt.Errorf("cannot locate %s in JSON source", match.Path)
			}
			span.text, err = renderJSON(replacements[i], data[span.start:span.end], lineIndent(data, span.start))
			if err != nil {
				return nil, err
			}
			edits[i] = span
		}
	case "yaml":
		var document yaml.Node
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		if err := decoder.Decode(&document); err != nil {
			return nil, err
		}
		for i, match := range matches {
			node, flow, err := yamlNodeAt(&document, match.Path)
			if err != nil {
				return nil, err
			}
			start, end, err := yamlNodeSpan(data, node, flow)
			if err != nil {
				return nil, fmt.Errorf("locating %s: %w", match.Path, err)
			}
			text, err := renderYAML(replacements[i], node, node.Column-1, len(lineIndent(data, start)))
			if err != nil {
				return nil, fmt.Errorf("encoding replacement for %s: %w", match.Path, err)
			}
			edits[i] = textEdit{start: start, end: end, text: text}
		}
	case "toml":
		spans, err := tomlSpans(data)
		if err != nil {
			return nil, err
		}
		for i, match := range matches {
			span, ok := spans[match.Path.Pointer()]
			if !ok {
				return nil, fmt.Errorf("cannot safely locate %s in TOML source", match.Path)
			}
			span.text, err = renderTOML(replacements[i])
			if err != nil {
				return nil, fmt.Errorf("encoding replacement for %s: %w", match.Path, err)
			}
			edits[i] = span
		}
	default:
		return nil, fmt.Errorf("unsupported format %q", format)
	}
	return applyTextEdits(data, edits)
}

// applyTextEdits splices every replacement into data in one pass. Validating
// the spans up front also yields the exact output size, so the document is
// copied once however many spans are replaced.
func applyTextEdits(data []byte, edits []textEdit) ([]byte, error) {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	size := len(data)
	previous := 0
	for _, edit := range edits {
		if edit.start < 0 || edit.end < edit.start || edit.end > len(data) {
			return nil, fmt.Errorf("invalid source span")
		}
		if edit.start < previous {
			return nil, fmt.Errorf("selected source spans overlap")
		}
		previous = edit.end
		size += len(edit.text) - (edit.end - edit.start)
	}
	result := make([]byte, 0, size)
	offset := 0
	for _, edit := range edits {
		result = append(result, data[offset:edit.start]...)
		result = append(result, edit.text...)
		offset = edit.end
	}
	return append(result, data[offset:]...), nil
}

func mergeDeletionEdits(edits []textEdit) []textEdit {
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	result := make([]textEdit, 0, len(edits))
	for _, edit := range edits {
		if len(result) > 0 && len(edit.text) == 0 && len(result[len(result)-1].text) == 0 && edit.start <= result[len(result)-1].end {
			result[len(result)-1].end = max(result[len(result)-1].end, edit.end)
			continue
		}
		result = append(result, edit)
	}
	return result
}

type jsonSpanParser struct {
	data  []byte
	pos   int
	spans map[string]textEdit
}

func (p *jsonSpanParser) parse() error {
	p.skipSpace()
	if err := p.value(""); err != nil {
		return err
	}
	p.skipSpace()
	if p.pos != len(p.data) {
		return fmt.Errorf("unexpected JSON content at byte %d", p.pos+1)
	}
	return nil
}

func (p *jsonSpanParser) value(pointer string) error {
	p.skipSpace()
	start := p.pos
	if p.pos >= len(p.data) {
		return fmt.Errorf("unexpected end of JSON")
	}
	switch p.data[p.pos] {
	case '{':
		p.pos++
		p.skipSpace()
		if p.take('}') {
			p.spans[pointer] = textEdit{start: start, end: p.pos}
			return nil
		}
		for {
			p.skipSpace()
			raw, err := p.stringToken()
			if err != nil {
				return err
			}
			var key string
			if err := json.Unmarshal(raw, &key); err != nil {
				return err
			}
			p.skipSpace()
			if !p.take(':') {
				return fmt.Errorf("expected colon at byte %d", p.pos+1)
			}
			if err := p.value(pointer + "/" + pointerEscape(key)); err != nil {
				return err
			}
			p.skipSpace()
			if p.take('}') {
				break
			}
			if !p.take(',') {
				return fmt.Errorf("expected comma at byte %d", p.pos+1)
			}
		}
	case '[':
		p.pos++
		p.skipSpace()
		if p.take(']') {
			p.spans[pointer] = textEdit{start: start, end: p.pos}
			return nil
		}
		for index := 0; ; index++ {
			if err := p.value(pointer + "/" + strconv.Itoa(index)); err != nil {
				return err
			}
			p.skipSpace()
			if p.take(']') {
				break
			}
			if !p.take(',') {
				return fmt.Errorf("expected comma at byte %d", p.pos+1)
			}
		}
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
	p.spans[pointer] = textEdit{start: start, end: p.pos}
	return nil
}

func (p *jsonSpanParser) stringToken() ([]byte, error) {
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

func (p *jsonSpanParser) skipSpace() {
	for p.pos < len(p.data) && bytes.ContainsRune([]byte(" \t\r\n"), rune(p.data[p.pos])) {
		p.pos++
	}
}
func (p *jsonSpanParser) take(want byte) bool {
	if p.pos < len(p.data) && p.data[p.pos] == want {
		p.pos++
		return true
	}
	return false
}

func renderJSON(value any, previous []byte, indent string) ([]byte, error) {
	compact, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if !bytes.Contains(previous, []byte{'\n'}) || (len(compact) > 0 && compact[0] != '{' && compact[0] != '[') {
		return compact, nil
	}
	var output bytes.Buffer
	if err := json.Indent(&output, compact, indent, "  "); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

// yamlNodeAt returns the node at path and whether it sits inside a flow
// collection, which decides how its plain scalar is terminated.
func yamlNodeAt(document *yaml.Node, path spec.NormalizedPath) (*yaml.Node, bool, error) {
	node := document
	if node.Kind == yaml.DocumentNode {
		if len(node.Content) == 0 {
			return nil, false, fmt.Errorf("empty YAML document")
		}
		node = node.Content[0]
	}
	flow := false
	for _, selector := range path {
		if node.Style&yaml.FlowStyle != 0 {
			flow = true
		}
		switch selector := selector.(type) {
		case spec.Name:
			if node.Kind != yaml.MappingNode {
				return nil, false, fmt.Errorf("%s reaches non-mapping YAML node", path)
			}
			found := false
			for i := 0; i+1 < len(node.Content); i += 2 {
				if node.Content[i].Value == string(selector) {
					node = node.Content[i+1]
					found = true
					break
				}
			}
			if !found {
				return nil, false, fmt.Errorf("cannot locate %s in YAML source", path)
			}
		case spec.Index:
			if node.Kind != yaml.SequenceNode || int(selector) < 0 || int(selector) >= len(node.Content) {
				return nil, false, fmt.Errorf("cannot locate %s in YAML source", path)
			}
			node = node.Content[int(selector)]
		}
		if node.Alias != nil {
			return nil, false, fmt.Errorf("editing YAML aliases is not supported")
		}
	}
	return node, flow, nil
}

func yamlNodeSpan(data []byte, node *yaml.Node, flow bool) (int, int, error) {
	start, err := lineColumnOffset(data, node.Line, node.Column)
	if err != nil {
		return 0, 0, err
	}
	if node.Kind == yaml.ScalarNode {
		return start, yamlScalarEnd(data, start, node, flow), nil
	}
	if start < len(data) && (data[start] == '[' || data[start] == '{') {
		return start, balancedEnd(data, start, data[start]), nil
	}
	end := start
	for _, child := range node.Content {
		_, childEnd, err := yamlNodeSpan(data, child, flow)
		if err == nil && childEnd > end {
			end = childEnd
		}
	}
	return start, end, nil
}

// yamlKeyEnd returns the offset just past a mapping key. A plain scalar renders
// verbatim, so scanning for a value terminator would run past the colon and
// swallow the value the key introduces.
func yamlKeyEnd(data []byte, start int, node *yaml.Node, flow bool) int {
	if start < len(data) && data[start] != '\'' && data[start] != '"' &&
		node.Style != yaml.LiteralStyle && node.Style != yaml.FoldedStyle &&
		bytes.HasPrefix(data[start:], []byte(node.Value)) {
		return start + len(node.Value)
	}
	return yamlScalarEnd(data, start, node, flow)
}

func yamlScalarEnd(data []byte, start int, node *yaml.Node, flow bool) int {
	if node.Style == yaml.LiteralStyle || node.Style == yaml.FoldedStyle {
		return yamlBlockScalarEnd(data, start)
	}
	if start >= len(data) {
		return start
	}
	quote := byte(0)
	scanStart := start
	flowDepth := 0
	if data[start] == '\'' || data[start] == '"' {
		quote = data[start]
		scanStart++
	}
	for i := scanStart; i < len(data); i++ {
		c := data[i]
		if quote != 0 {
			if quote == '"' && c == '\\' {
				i++
				continue
			}
			if c == quote {
				if quote == '\'' && i+1 < len(data) && data[i+1] == '\'' {
					i++
					continue
				}
				return i + 1
			}
			continue
		}
		switch c {
		case '[', '{':
			flowDepth++
		case '#':
			if flowDepth == 0 && (i == start || data[i-1] == ' ' || data[i-1] == '\t') {
				return trimSpaceEnd(data, start, i)
			}
		case ',':
			if flowDepth == 0 && flow {
				return trimSpaceEnd(data, start, i)
			}
		case ']', '}':
			if flowDepth == 0 && flow {
				return trimSpaceEnd(data, start, i)
			}
		case '\r', '\n':
			if flowDepth == 0 {
				return trimSpaceEnd(data, start, i)
			}
		}
	}
	return trimSpaceEnd(data, start, len(data))
}

// yamlBlockScalarEnd returns the offset just before the newline that closes a
// literal or folded scalar. Its content is every following line indented deeper
// than the line carrying the block marker.
func yamlBlockScalarEnd(data []byte, start int) int {
	outer := len(lineIndent(data, start))
	lineEnd := bytes.IndexByte(data[start:], '\n')
	if lineEnd < 0 {
		return len(data)
	}
	end := start + lineEnd
	for end < len(data) {
		next := end + 1
		limit := len(data)
		if offset := bytes.IndexByte(data[next:], '\n'); offset >= 0 {
			limit = next + offset
		}
		line := data[next:limit]
		if len(bytes.TrimSpace(line)) > 0 && leadingSpaces(line) <= outer {
			break
		}
		end = limit
	}
	return end
}

// renderYAML encodes value in place of old. indent is the column the node
// starts at, which continuation lines line up with; outer is the indentation of
// the line carrying it, which a block scalar's content nests under instead.
func renderYAML(value any, old *yaml.Node, indent, outer int) ([]byte, error) {
	var node yaml.Node
	if err := node.Encode(value); err != nil {
		return nil, err
	}
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = *node.Content[0]
	}
	if old.Kind == yaml.ScalarNode && node.Kind == yaml.ScalarNode {
		if node.Tag == old.Tag {
			node.Style = old.Style
		}
		var output bytes.Buffer
		encoder := yaml.NewEncoder(&output)
		encoder.SetIndent(2)
		if err := encoder.Encode(&node); err != nil {
			return nil, err
		}
		_ = encoder.Close()
		// The encoder terminates the document with a newline, and folds the
		// final line break of a folded scalar into an extra blank line. The
		// span stops before the newline that closes the node, so drop both.
		encoded := bytes.TrimSuffix(output.Bytes(), []byte{'\n'})
		if len(encoded) > 0 && encoded[0] == '>' {
			encoded = bytes.TrimSuffix(encoded, []byte{'\n'})
		}
		if len(encoded) > 0 && (encoded[0] == '|' || encoded[0] == '>') {
			return reindent(encoded, outer), nil
		}
		return reindent(encoded, indent), nil
	}
	data, err := yaml.Marshal(value)
	if err != nil {
		return nil, err
	}
	return reindent(bytes.TrimSuffix(data, []byte{'\n'}), indent), nil
}

// reindent shifts the continuation lines of a multi-line encoding to the column
// the replaced node started at; the encoder always renders from column zero.
func reindent(data []byte, indent int) []byte {
	if indent <= 0 || !bytes.Contains(data, []byte{'\n'}) {
		return data
	}
	prefix := []byte(strings.Repeat(" ", indent))
	lines := bytes.Split(data, []byte{'\n'})
	for i := 1; i < len(lines); i++ {
		if len(lines[i]) > 0 {
			lines[i] = append(append([]byte(nil), prefix...), lines[i]...)
		}
	}
	return bytes.Join(lines, []byte{'\n'})
}

func lineColumnOffset(data []byte, line, column int) (int, error) {
	if line < 1 || column < 1 {
		return 0, fmt.Errorf("invalid line or column")
	}
	offset := 0
	for current := 1; current < line; current++ {
		next := bytes.IndexByte(data[offset:], '\n')
		if next < 0 {
			return 0, fmt.Errorf("line is outside source")
		}
		offset += next + 1
	}
	for current := 1; current < column; current++ {
		if offset >= len(data) {
			return 0, fmt.Errorf("column is outside source")
		}
		_, size := utf8.DecodeRune(data[offset:])
		offset += size
	}
	return offset, nil
}

func balancedEnd(data []byte, start int, opener byte) int {
	closer := byte(']')
	if opener == '{' {
		closer = '}'
	}
	depth := 0
	quote := byte(0)
	for i := start; i < len(data); i++ {
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
		if c == opener {
			depth++
		}
		if c == closer {
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(data)
}

func tomlSpans(data []byte) (map[string]textEdit, error) {
	spans := make(map[string]textEdit)
	table := []string{}
	arrayNext := make(map[string]int)
	arrayCurrent := make(map[string]int)
	for offset := 0; offset < len(data); {
		lineEnd := bytes.IndexByte(data[offset:], '\n')
		if lineEnd < 0 {
			lineEnd = len(data) - offset
		}
		line := data[offset : offset+lineEnd]
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 || trimmed[0] == '#' {
			offset += lineEnd
			if offset < len(data) {
				offset++
			}
			continue
		}
		if trimmed[0] == '[' {
			array := len(trimmed) > 1 && trimmed[1] == '['
			closing := []byte("]")
			if array {
				closing = []byte("]]")
			}
			end := tomlHeaderEnd(trimmed, closing)
			if end < 0 {
				return nil, fmt.Errorf("unterminated TOML table header")
			}
			startName := 1
			if array {
				startName = 2
			}
			parts, err := tomlKeyParts(string(bytes.TrimSpace(trimmed[startName:end])))
			if err != nil {
				return nil, err
			}
			if !array {
				table = expandArrayPath(parts, arrayCurrent)
			} else {
				// The last segment names the array being appended to, so only the
				// prefix leading to it is resolved against existing elements.
				table = append(expandArrayPath(parts[:len(parts)-1], arrayCurrent), parts[len(parts)-1])
				key := "/" + strings.Join(escapeParts(table), "/")
				index := arrayNext[key]
				arrayNext[key] = index + 1
				arrayCurrent[key] = index
				table = append(table, strconv.Itoa(index))
			}
			offset += lineEnd
			if offset < len(data) {
				offset++
			}
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
		start := offset + eq + 1
		for start < len(data) && (data[start] == ' ' || data[start] == '\t') {
			start++
		}
		end := tomlValueEnd(data, start)
		pointerParts := append(append([]string(nil), table...), keys...)
		spans["/"+strings.Join(escapeParts(pointerParts), "/")] = textEdit{start: start, end: end}
		offset = end
		for offset < len(data) && data[offset] != '\n' {
			offset++
		}
		if offset < len(data) {
			offset++
		}
	}
	return spans, nil
}

// tomlHeaderEnd locates the closing bracket sequence of a table header, skipping any
// that sits inside a quoted key. Scanning for the first "]" instead meant a single key
// such as ["we]ird"] truncated the name and made the whole file unreadable.
func tomlHeaderEnd(header []byte, closing []byte) int {
	quote := byte(0)
	for i := 0; i < len(header); i++ {
		c := header[i]
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
		if bytes.HasPrefix(header[i:], closing) {
			return i
		}
	}
	return -1
}

// expandArrayPath rewrites a table path so every prefix naming an array of tables
// carries the index of its most recent element. TOML resolves [items.sub] against the
// last [[items]], so without this the sub-table was recorded under /items/sub, a path
// the decoded document does not contain, while its real location stayed unreachable.
func expandArrayPath(parts []string, current map[string]int) []string {
	expanded := make([]string, 0, len(parts)+1)
	for _, part := range parts {
		expanded = append(expanded, part)
		if index, ok := current["/"+strings.Join(escapeParts(expanded), "/")]; ok {
			expanded = append(expanded, strconv.Itoa(index))
		}
	}
	return expanded
}

func tomlEquals(line []byte) int {
	quote := byte(0)
	for i := 0; i < len(line); i++ {
		c := line[i]
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
		if c == '=' {
			return i
		}
	}
	return -1
}

func tomlValueEnd(data []byte, start int) int {
	depth := 0
	quote := byte(0)
	triple := false
	for i := start; i < len(data); i++ {
		c := data[i]
		if quote != 0 {
			if triple && i+2 < len(data) && data[i] == quote && data[i+1] == quote && data[i+2] == quote {
				return i + 3
			}
			if quote == '"' && c == '\\' {
				i++
				continue
			}
			if !triple && c == quote {
				quote = 0
			}
			continue
		}
		if c == '\'' || c == '"' {
			quote = c
			triple = i+2 < len(data) && data[i+1] == c && data[i+2] == c
			if triple {
				i += 2
			}
			continue
		}
		switch c {
		case '[', '{':
			depth++
		case ']', '}':
			if depth > 0 {
				depth--
			}
		case '#':
			if depth == 0 {
				return trimSpaceEnd(data, start, i)
			}
		case '\r', '\n':
			if depth == 0 {
				return trimSpaceEnd(data, start, i)
			}
		}
	}
	return trimSpaceEnd(data, start, len(data))
}

func tomlKeyParts(key string) ([]string, error) {
	parts := []string{}
	start := 0
	quote := byte(0)
	for i := 0; i <= len(key); i++ {
		if i < len(key) {
			c := key[i]
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
			if c != '.' {
				continue
			}
		}
		part := strings.TrimSpace(key[start:i])
		if part == "" {
			return nil, fmt.Errorf("empty TOML key")
		}
		if part[0] == '"' {
			var decoded string
			if err := json.Unmarshal([]byte(part), &decoded); err != nil {
				return nil, err
			}
			part = decoded
		} else if part[0] == '\'' {
			if len(part) < 2 || part[len(part)-1] != '\'' {
				return nil, fmt.Errorf("invalid quoted TOML key")
			}
			part = part[1 : len(part)-1]
		}
		parts = append(parts, part)
		start = i + 1
	}
	return parts, nil
}

// tomlString encodes a TOML basic string. strconv.Quote produces Go syntax, which
// defines escapes TOML does not have: a string holding a vertical tab or any other
// control character came out as \v or \x01, and the file it was written into no
// longer parsed. TOML allows only \b, \t, \n, \f, \r, \", \\ and the \u/\U forms.
func tomlString(value string) ([]byte, error) {
	if !utf8.ValidString(value) {
		return nil, fmt.Errorf("TOML strings must be valid UTF-8")
	}
	var encoded strings.Builder
	encoded.Grow(len(value) + 2)
	encoded.WriteByte('"')
	for _, symbol := range value {
		switch symbol {
		case '"':
			encoded.WriteString(`\"`)
		case '\\':
			encoded.WriteString(`\\`)
		case '\b':
			encoded.WriteString(`\b`)
		case '\t':
			encoded.WriteString(`\t`)
		case '\n':
			encoded.WriteString(`\n`)
		case '\f':
			encoded.WriteString(`\f`)
		case '\r':
			encoded.WriteString(`\r`)
		default:
			// The remaining control characters, and DEL, have no shorthand and must
			// travel as escapes; everything else is written literally.
			if symbol < 0x20 || symbol == 0x7f {
				fmt.Fprintf(&encoded, `\u%04X`, symbol)
				continue
			}
			encoded.WriteRune(symbol)
		}
	}
	encoded.WriteByte('"')
	return []byte(encoded.String()), nil
}

// tomlFloat keeps a float looking like one. FormatFloat renders a whole value as "3",
// which TOML reads back as an integer, so the edit silently changed the value's type.
// TOML spells the non-finite values in lower case and without a leading plus.
func tomlFloat(value float64) string {
	switch {
	case math.IsNaN(value):
		return "nan"
	case math.IsInf(value, 1):
		return "inf"
	case math.IsInf(value, -1):
		return "-inf"
	}
	text := strconv.FormatFloat(value, 'g', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return text
}

func renderTOML(value any) ([]byte, error) {
	switch value := value.(type) {
	case nil:
		return nil, fmt.Errorf("TOML has no null value")
	case string:
		return tomlString(value)
	case bool:
		return []byte(strconv.FormatBool(value)), nil
	case json.Number:
		return []byte(value.String()), nil
	case int:
		return []byte(strconv.Itoa(value)), nil
	case int64:
		return []byte(strconv.FormatInt(value, 10)), nil
	case float64:
		return []byte(tomlFloat(value)), nil
	case time.Time:
		return []byte(value.Format(time.RFC3339Nano)), nil
	case []any:
		parts := make([]string, len(value))
		for i, child := range value {
			encoded, err := renderTOML(child)
			if err != nil {
				return nil, err
			}
			parts[i] = string(encoded)
		}
		return []byte("[" + strings.Join(parts, ", ") + "]"), nil
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, len(keys))
		for i, key := range keys {
			encoded, err := renderTOML(value[key])
			if err != nil {
				return nil, err
			}
			// The key needs TOML quoting for the same reason the value does.
			quoted, err := tomlString(key)
			if err != nil {
				return nil, err
			}
			parts[i] = string(quoted) + " = " + string(encoded)
		}
		return []byte("{" + strings.Join(parts, ", ") + "}"), nil
	default:
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		return data, nil
	}
}

func pointerEscape(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}
func escapeParts(parts []string) []string {
	escaped := make([]string, len(parts))
	for i, part := range parts {
		escaped[i] = pointerEscape(part)
	}
	return escaped
}
func trimSpaceEnd(data []byte, start, end int) int {
	for end > start && (data[end-1] == ' ' || data[end-1] == '\t') {
		end--
	}
	return end
}
func leadingSpaces(line []byte) int {
	count := 0
	for count < len(line) && line[count] == ' ' {
		count++
	}
	return count
}
func lineIndent(data []byte, offset int) string {
	start := bytes.LastIndexByte(data[:offset], '\n') + 1
	end := start
	for end < offset && (data[end] == ' ' || data[end] == '\t') {
		end++
	}
	return string(data[start:end])
}
