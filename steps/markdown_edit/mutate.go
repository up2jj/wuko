package markdownedit

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/yuin/goldmark/v2/ast"
	extast "github.com/yuin/goldmark/v2/extension/ast"
)

type patch struct {
	start       int
	end         int
	replacement []byte
	order       int
	description string
}

func (d *document) plan(editIndex, matchIndex int, edit *Edit, match *nodeRef, value any) ([]patch, bool, error) {
	order := editIndex*1_000_000 + matchIndex*1_000
	if match.kind == "table_cell" && edit.Operation != "set" {
		return nil, false, fmt.Errorf("table_cell supports only operation set")
	}
	if match.kind == "table_row" {
		if match.header {
			return nil, false, fmt.Errorf("table row mutations support body rows only")
		}
		if !oneOf(edit.Operation, "replace", "insert", "delete") {
			return nil, false, fmt.Errorf("table_row supports replace, insert, or delete")
		}
	}
	var patches []patch
	var err error
	switch edit.Operation {
	case "set":
		patches, err = d.planSet(match, edit.Field, value, order)
	case "replace":
		patches, err = d.planReplace(match, value, order)
	case "delete":
		patches = []patch{d.newPatch(match.start, match.end, nil, order, "delete "+match.kind)}
	case "insert":
		patches, err = d.planInsert(match, edit.Position, value, order)
	case "prepend", "append":
		patches, err = d.planInside(match, edit.Operation, value, order)
	}
	if err != nil {
		return nil, false, err
	}
	changed := false
	result := patches[:0]
	for _, item := range patches {
		if bytes.Equal(d.source[item.start:item.end], item.replacement) {
			continue
		}
		changed = true
		result = append(result, item)
	}
	return result, changed, nil
}

func (d *document) planSet(match *nodeRef, field string, value any, order int) ([]patch, error) {
	switch node := match.node.(type) {
	case *ast.Heading:
		return d.setHeading(match, node, field, value, order)
	case *ast.CodeBlock:
		return d.setCodeBlock(match, node, field, value, order)
	case *ast.Link:
		return d.setLink(match, field, value, order, false)
	case *ast.Image:
		return d.setLink(match, field, value, order, true)
	case *ast.ListItem:
		if field != "checked" {
			return nil, fmt.Errorf("field %q is not supported for list_item", field)
		}
		if !extensionTask(match) {
			return nil, fmt.Errorf("checked is supported only for an existing task item")
		}
		checked, ok := value.(bool)
		if !ok {
			return nil, fmt.Errorf("checked value must be boolean, got %T", value)
		}
		start, ok := taskMarker(d.source, match.start, match.end)
		if !ok {
			return nil, fmt.Errorf("task marker could not be located")
		}
		marker := byte(' ')
		if checked {
			marker = 'x'
		}
		return []patch{d.newPatch(start, start+1, []byte{marker}, order, "set task checked")}, nil
	case *extast.TableCell:
		if field != "content" {
			return nil, fmt.Errorf("field %q is not supported for table_cell", field)
		}
		content, err := stringValue(value, "table cell content")
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(content, "\r\n") {
			return nil, fmt.Errorf("table cell content must be single-line")
		}
		start, end := cellContentSpan(d.source, node)
		return []patch{d.newPatch(start, end, []byte(escapeTableCell(content)), order, "set table cell")}, nil
	default:
		return nil, fmt.Errorf("operation set is not supported for %s", match.kind)
	}
}

func extensionTask(match *nodeRef) bool { return match.task }

func (d *document) setHeading(match *nodeRef, heading *ast.Heading, field string, value any, order int) ([]patch, error) {
	if field == "text" {
		text, err := stringValue(value, "heading text")
		if err != nil {
			return nil, err
		}
		text = normalizeLineEndings(text, preferredLineEnding(d.source))
		start, end := headingTextSpan(d.source, heading, match)
		return []patch{d.newPatch(start, end, []byte(text), order, "set heading text")}, nil
	}
	if field == "level" {
		level, ok := integerValue(value)
		if !ok || level < 1 || level > 6 {
			return nil, fmt.Errorf("heading level must be an integer between 1 and 6")
		}
		if heading.HeadingKind == ast.HeadingKindATX {
			markerStart := firstNonSpace(d.source, match.start, lineEndWithoutNewline(d.source, match.start))
			markerEnd := markerStart
			for markerEnd < len(d.source) && d.source[markerEnd] == '#' {
				markerEnd++
			}
			return []patch{d.newPatch(markerStart, markerEnd, []byte(strings.Repeat("#", level)), order, "set heading level")}, nil
		}
		if level <= 2 {
			_, textEnd := headingTextSpan(d.source, heading, match)
			underlineStart := endOfCoveredLine(d.source, textEnd)
			underlineEnd := lineEndWithoutNewline(d.source, underlineStart)
			marker := byte('-')
			if level == 1 {
				marker = '='
			}
			width := max(3, underlineEnd-underlineStart)
			return []patch{d.newPatch(underlineStart, underlineEnd, bytes.Repeat([]byte{marker}, width), order, "set heading level")}, nil
		}
		ending := lineEnding(d.source[match.start:match.end])
		attributes := serializeAttributes(match.attrs)
		// An ATX heading is a single line, so fold the soft breaks a multi-line
		// setext heading may contain.
		text := strings.Join(strings.Fields(match.content()), " ")
		replacement := strings.Repeat("#", level) + " " + text + attributes + ending
		return []patch{d.newPatch(match.start, match.end, []byte(replacement), order, "convert setext heading")}, nil
	}
	if strings.HasPrefix(field, "attribute.") {
		name := strings.TrimPrefix(field, "attribute.")
		if name == "" || strings.ContainsAny(name, " \t\r\n={}") {
			return nil, fmt.Errorf("invalid heading attribute name %q", name)
		}
		updated := make(map[string]string, len(match.attrs)+1)
		for key, existing := range match.attrs {
			updated[key] = existing
		}
		if value == nil {
			delete(updated, name)
		} else {
			text, err := stringValue(value, "heading attribute")
			if err != nil {
				return nil, err
			}
			updated[name] = text
		}
		start, end, exists := headingAttributeSpan(d.source, heading, match)
		replacement := serializeAttributes(updated)
		if exists {
			return []patch{d.newPatch(start, end, []byte(replacement), order, "set heading attribute")}, nil
		}
		return []patch{d.newPatch(start, start, []byte(replacement), order, "add heading attribute")}, nil
	}
	return nil, fmt.Errorf("field %q is not supported for heading", field)
}

func (d *document) setCodeBlock(match *nodeRef, block *ast.CodeBlock, field string, value any, order int) ([]patch, error) {
	if block.CodeBlockKind != ast.CodeBlockKindFenced {
		return nil, fmt.Errorf("typed code block fields require a fenced code block")
	}
	fence, ok := scanFence(d.source, match.start, match.end)
	if !ok {
		return nil, fmt.Errorf("fence could not be located")
	}
	switch field {
	case "content":
		content, err := stringValue(value, "code block content")
		if err != nil {
			return nil, err
		}
		content = strings.ReplaceAll(content, "\r\n", "\n")
		content = strings.ReplaceAll(content, "\r", "\n")
		if content != "" && !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		content = prefixLines(content, fence.prefix)
		content = normalizeLineEndings(content, preferredLineEnding(d.source))
		patches := []patch{d.newPatch(fence.contentStart, fence.contentEnd, []byte(content), order, "set code content")}
		needed := max(fence.length, longestFenceRun(content, fence.character)+1)
		if needed > fence.length {
			marker := bytes.Repeat([]byte{fence.character}, needed)
			patches = append(patches,
				d.newPatch(fence.openMarkerStart, fence.openMarkerEnd, marker, order+1, "lengthen opening fence"))
			if fence.closeMarkerStart >= 0 {
				patches = append(patches,
					d.newPatch(fence.closeMarkerStart, fence.closeMarkerEnd, marker, order+2, "lengthen closing fence"))
			}
		}
		return patches, nil
	case "info":
		info, err := stringValue(value, "code block info")
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(info, "\r\n") {
			return nil, fmt.Errorf("code block info must be single-line")
		}
		return []patch{d.newPatch(fence.infoStart, fence.infoEnd, []byte(info), order, "set code info")}, nil
	case "language":
		language, err := stringValue(value, "code block language")
		if err != nil {
			return nil, err
		}
		if strings.ContainsAny(language, " \t\r\n") {
			return nil, fmt.Errorf("code block language must be one word")
		}
		start, end := fence.infoStart, fence.infoStart
		for end < fence.infoEnd && !unicode.IsSpace(rune(d.source[end])) {
			end++
		}
		return []patch{d.newPatch(start, end, []byte(language), order, "set code language")}, nil
	default:
		return nil, fmt.Errorf("field %q is not supported for code_block", field)
	}
}

func (d *document) setLink(match *nodeRef, field string, value any, order int, image bool) ([]patch, error) {
	parts, ok := scanLink(d.source, match.start, match.end)
	if !ok || !parts.inline {
		return nil, fmt.Errorf("typed fields are supported only for inline links and images")
	}
	labelField := "text"
	destinationField := "destination"
	if image {
		labelField = "alt"
		destinationField = "source"
	}
	switch field {
	case labelField:
		text, err := stringValue(value, labelField)
		if err != nil {
			return nil, err
		}
		return []patch{d.newPatch(parts.labelStart, parts.labelEnd, []byte(text), order, "set "+labelField)}, nil
	case destinationField:
		text, err := stringValue(value, destinationField)
		if err != nil {
			return nil, err
		}
		return []patch{d.newPatch(parts.destinationStart, parts.destinationEnd, []byte(text), order, "set "+destinationField)}, nil
	case "title":
		if value == nil {
			if parts.titleStart < 0 {
				return nil, nil
			}
			return []patch{d.newPatch(parts.titleOuterStart, parts.titleOuterEnd, nil, order, "remove link title")}, nil
		}
		text, err := stringValue(value, "link title")
		if err != nil {
			return nil, err
		}
		if parts.titleStart >= 0 {
			return []patch{d.newPatch(parts.titleStart, parts.titleEnd, []byte(escapeQuoted(text)), order, "set link title")}, nil
		}
		return []patch{d.newPatch(parts.closeParen, parts.closeParen, []byte(` "`+escapeQuoted(text)+`"`), order, "add link title")}, nil
	default:
		return nil, fmt.Errorf("field %q is not supported for %s", field, match.kind)
	}
}

func (d *document) planReplace(match *nodeRef, value any, order int) ([]patch, error) {
	if match.kind == "table_row" {
		row, err := tableRowValue(value)
		if err != nil {
			return nil, err
		}
		replacement, err := d.renderTableRow(match, row)
		if err != nil {
			return nil, err
		}
		return []patch{d.newPatch(match.start, match.end, replacement, order, "replace table row")}, nil
	}
	text, err := stringValue(value, "replacement")
	if err != nil {
		return nil, err
	}
	if !isInlineNode(match.node) && match.kind != "document" {
		text = normalizeLineEndings(text, preferredLineEnding(d.source))
		text = ensureLineTerminated(text, lineEnding(d.source[match.start:match.end]))
	}
	return []patch{d.newPatch(match.start, match.end, []byte(text), order, "replace "+match.kind)}, nil
}

func (d *document) planInsert(match *nodeRef, position string, value any, order int) ([]patch, error) {
	if match.kind == "table_row" {
		row, err := tableRowValue(value)
		if err != nil {
			return nil, err
		}
		replacement, err := d.renderTableRow(match, row)
		if err != nil {
			return nil, err
		}
		offset := match.start
		if position == "after" {
			offset = match.end
		}
		replacement = blockInsertion(d.source, offset, replacement, preferredLineEnding(d.source))
		return []patch{d.newPatch(offset, offset, replacement, order, "insert table row")}, nil
	}
	text, err := stringValue(value, "insert value")
	if err != nil {
		return nil, err
	}
	if !isInlineNode(match.node) {
		text = normalizeLineEndings(text, preferredLineEnding(d.source))
		text = ensureLineTerminated(text, preferredLineEnding(d.source))
	}
	offset := match.start
	if position == "after" {
		offset = match.end
	}
	if !isInlineNode(match.node) {
		text = string(blockInsertion(d.source, offset, []byte(text), preferredLineEnding(d.source)))
	}
	return []patch{d.newPatch(offset, offset, []byte(text), order, "insert "+position)}, nil
}

func (d *document) planInside(match *nodeRef, operation string, value any, order int) ([]patch, error) {
	if match.kind == "table" {
		row, err := tableRowValue(value)
		if err != nil {
			return nil, err
		}
		replacement, err := d.renderTableRow(match, row)
		if err != nil {
			return nil, err
		}
		offset := match.end
		if operation == "prepend" {
			offset = tableBodyStart(d.source, match.start, match.end)
		}
		replacement = blockInsertion(d.source, offset, replacement, preferredLineEnding(d.source))
		return []patch{d.newPatch(offset, offset, replacement, order, operation+" table row")}, nil
	}
	text, err := stringValue(value, operation+" value")
	if err != nil {
		return nil, err
	}
	if heading, ok := match.node.(*ast.Heading); ok {
		start, end := headingTextSpan(d.source, heading, match)
		offset := end
		if operation == "prepend" {
			offset = start
		}
		text = normalizeLineEndings(text, preferredLineEnding(d.source))
		return []patch{d.newPatch(offset, offset, []byte(text), order, operation+" heading text")}, nil
	}
	if paragraph, ok := match.node.(*ast.Paragraph); ok {
		segments := paragraph.Source()
		if len(segments) > 0 {
			offset := segments[len(segments)-1].Stop
			if operation == "prepend" {
				offset = segments[0].Start
			}
			text = normalizeLineEndings(text, preferredLineEnding(d.source))
			return []patch{d.newPatch(offset, offset, []byte(text), order, operation+" paragraph")}, nil
		}
	}
	if _, ok := match.node.(*ast.Link); ok {
		return d.planInsideLink(match, operation, text, order)
	}
	if _, ok := match.node.(*ast.Image); ok {
		return d.planInsideLink(match, operation, text, order)
	}
	if block, ok := match.node.(*ast.CodeBlock); ok && block.CodeBlockKind == ast.CodeBlockKindFenced {
		fence, found := scanFence(d.source, match.start, match.end)
		if !found {
			return nil, fmt.Errorf("fence could not be located")
		}
		text = normalizeLineEndings(text, "\n")
		if text != "" && !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text = prefixLines(text, fence.prefix)
		text = normalizeLineEndings(text, preferredLineEnding(d.source))
		offset := fence.contentEnd
		if operation == "prepend" {
			offset = fence.contentStart
		}
		patches := []patch{d.newPatch(offset, offset, []byte(text), order, operation+" code content")}
		needed := max(fence.length, longestFenceRun(text, fence.character)+1)
		if needed > fence.length {
			marker := bytes.Repeat([]byte{fence.character}, needed)
			patches = append(patches,
				d.newPatch(fence.openMarkerStart, fence.openMarkerEnd, marker, order+1, "lengthen opening fence"))
			if fence.closeMarkerStart >= 0 {
				patches = append(patches,
					d.newPatch(fence.closeMarkerStart, fence.closeMarkerEnd, marker, order+2, "lengthen closing fence"))
			}
		}
		return patches, nil
	}
	if !isInlineNode(match.node) {
		text = normalizeLineEndings(text, preferredLineEnding(d.source))
		text = ensureLineTerminated(text, preferredLineEnding(d.source))
	}
	offset := match.end
	if operation == "prepend" {
		offset = match.start
	}
	if !isInlineNode(match.node) {
		text = string(blockInsertion(d.source, offset, []byte(text), preferredLineEnding(d.source)))
	}
	return []patch{d.newPatch(offset, offset, []byte(text), order, operation+" "+match.kind)}, nil
}

func (d *document) planInsideLink(match *nodeRef, operation, value string, order int) ([]patch, error) {
	parts, ok := scanLink(d.source, match.start, match.end)
	if !ok {
		return nil, fmt.Errorf("link content could not be located")
	}
	offset := parts.labelEnd
	if operation == "prepend" {
		offset = parts.labelStart
	}
	return []patch{d.newPatch(offset, offset, []byte(value), order, operation+" "+match.kind+" content")}, nil
}

func (d *document) renderTableRow(match *nodeRef, values []string) ([]byte, error) {
	table := tableAncestor(match.node)
	if match.kind == "table" {
		table, _ = match.node.(*extast.Table)
	}
	if table == nil {
		return nil, fmt.Errorf("table row has no table ancestor")
	}
	width := len(tableHeaders(table, d.source))
	if len(values) != width {
		return nil, fmt.Errorf("table row has %d cells, want %d", len(values), width)
	}
	templateStart := match.start
	if match.kind == "table" {
		templateStart = lineStart(d.source, table.Pos())
		body := table.LastChild()
		if body != nil && body.Kind() == extast.KindTableBody && body.LastChild() != nil {
			templateStart = lineStart(d.source, body.LastChild().Pos())
		}
	}
	templateEnd := lineEndWithoutNewline(d.source, templateStart)
	template := d.source[templateStart:templateEnd]
	// Reuse the template row's indentation and blockquote markers so a rendered
	// row stays inside the list item or quote that contains the table.
	prefixEnd := 0
	for prefixEnd < len(template) && (template[prefixEnd] == ' ' || template[prefixEnd] == '\t' || template[prefixEnd] == '>') {
		prefixEnd++
	}
	prefix := string(template[:prefixEnd])
	leading := prefixEnd < len(template) && template[prefixEnd] == '|'
	trimmed := bytes.TrimSpace(template)
	trailing := len(trimmed) > 0 && trimmed[len(trimmed)-1] == '|'
	separator := " | "
	if !bytes.Contains(template, []byte(" | ")) {
		separator = "|"
	}
	parts := make([]string, len(values))
	for index, value := range values {
		parts[index] = escapeTableCell(value)
	}
	row := strings.Join(parts, separator)
	if leading {
		row = "| " + row
	}
	if trailing {
		row += " |"
	}
	return []byte(prefix + row + lineEnding(d.source[templateStart:lineEnd(d.source, templateStart)])), nil
}

func (d *document) newPatch(start, end int, replacement []byte, order int, description string) patch {
	return patch{start: start, end: end, replacement: replacement, order: order, description: description}
}

func validatePatches(patches []patch) error {
	ordered := append([]patch(nil), patches...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].start != ordered[j].start {
			return ordered[i].start < ordered[j].start
		}
		return ordered[i].end < ordered[j].end
	})
	for left := range ordered {
		for right := left + 1; right < len(ordered); right++ {
			if ordered[right].start > ordered[left].end {
				break
			}
			if patchesConflict(ordered[left], ordered[right]) {
				return fmt.Errorf("Markdown edits conflict: %s [%d:%d] overlaps %s [%d:%d]",
					ordered[left].description, ordered[left].start, ordered[left].end,
					ordered[right].description, ordered[right].start, ordered[right].end)
			}
		}
	}
	return nil
}

func patchesConflict(left, right patch) bool {
	leftInsertion := left.start == left.end
	rightInsertion := right.start == right.end
	if leftInsertion && rightInsertion {
		return false
	}
	if leftInsertion {
		return right.start < left.start && left.start < right.end
	}
	if rightInsertion {
		return left.start < right.start && right.start < left.end
	}
	return left.start < right.end && right.start < left.end
}

func applyPatches(source []byte, patches []patch) []byte {
	ordered := append([]patch(nil), patches...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].start != ordered[j].start {
			return ordered[i].start < ordered[j].start
		}
		if ordered[i].end != ordered[j].end {
			// An insertion sharing a start with a replaced range must land before it.
			return ordered[i].end < ordered[j].end
		}
		return ordered[i].order < ordered[j].order
	})
	size := len(source)
	for _, item := range ordered {
		size += len(item.replacement) - (item.end - item.start)
	}
	result := make([]byte, 0, max(size, len(source)))
	cursor := 0
	for _, item := range ordered {
		if item.start > cursor {
			result = append(result, source[cursor:item.start]...)
			cursor = item.start
		}
		result = append(result, item.replacement...)
		if item.end > cursor {
			cursor = item.end
		}
	}
	return append(result, source[cursor:]...)
}

func nodeSpan(source []byte, node ast.Node) (int, int) {
	if _, ok := node.(*ast.Document); ok {
		return 0, len(source)
	}
	start := node.Pos()
	if start < 0 {
		start = minNodeStart(node)
	}
	if start < 0 {
		return -1, -1
	}
	switch value := node.(type) {
	case *ast.Link, *ast.Image, *ast.AutoLink:
		return inlineNodeSpan(source, node, start)
	case *ast.Heading:
		start = lineStart(source, start)
		end := lineEnd(source, start)
		if value.HeadingKind == ast.HeadingKindSetext {
			// A setext heading may carry several text lines; the span has to
			// cover all of them plus the trailing underline.
			textEnd := end
			if segments := value.Source(); len(segments) > 0 {
				textEnd = endOfCoveredLine(source, segments[len(segments)-1].Stop)
			}
			end = lineEnd(source, textEnd)
		}
		return start, end
	case *ast.CodeBlock:
		start = lineStart(source, start)
		if value.CodeBlockKind == ast.CodeBlockKindFenced {
			if fence, ok := scanFence(source, start, len(source)); ok {
				return start, fence.outerEnd
			}
		}
		return start, endOfCoveredLine(source, maxNodeStop(node, source))
	case *extast.TableCell:
		contentStart, contentEnd := cellContentSpan(source, value)
		return contentStart, contentEnd
	case *extast.TableHeader, *extast.TableRow:
		start = lineStart(source, start)
		return start, lineEnd(source, start)
	case *extast.Table:
		start = lineStart(source, start)
		end := lineEnd(source, maxNodeStop(node, source))
		if end <= lineEnd(source, start) {
			end = lineEnd(source, end)
		}
		return start, end
	default:
		return lineStart(source, start), endOfCoveredLine(source, maxNodeStop(node, source))
	}
}

func minNodeStart(node ast.Node) int {
	start := -1
	_ = ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering && child.Pos() >= 0 && (start < 0 || child.Pos() < start) {
			start = child.Pos()
		}
		return ast.WalkContinue, nil
	})
	return start
}

func maxNodeStop(node ast.Node, source []byte) int {
	stop := max(node.Pos(), 0)
	_ = ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		if block, ok := child.(ast.BlockNode); ok {
			for _, segment := range block.Source() {
				stop = max(stop, segment.Stop)
			}
		}
		switch value := child.(type) {
		case *ast.Text:
			if !value.Value.IsOwned() {
				stop = max(stop, value.Value.Index().Stop)
			}
		case *ast.CodeSpan:
			for _, index := range value.Value.Indices() {
				stop = max(stop, index.Stop)
			}
		case *ast.CodeBlock:
			for _, segment := range value.Value.Segments() {
				stop = max(stop, segment.Stop)
			}
		}
		return ast.WalkContinue, nil
	})
	return min(stop, len(source))
}

func inlineNodeSpan(source []byte, node ast.Node, start int) (int, int) {
	if _, ok := node.(*ast.AutoLink); ok {
		if source[start] == '<' {
			if end := bytes.IndexByte(source[start:], '>'); end >= 0 {
				return start, start + end + 1
			}
		}
		end := start
		for end < len(source) && !unicode.IsSpace(rune(source[end])) && !strings.ContainsRune(",;)", rune(source[end])) {
			end++
		}
		return start, end
	}
	if parts, ok := scanLink(source, start, lineEndWithoutNewline(source, start)); ok {
		return start, parts.end
	}
	return start, max(start, maxNodeStop(node, source))
}

func headingTextSpan(source []byte, heading *ast.Heading, match *nodeRef) (int, int) {
	segments := heading.Source()
	if len(segments) > 0 {
		return segments[0].Start, segments[len(segments)-1].Stop
	}
	if heading.HeadingKind == ast.HeadingKindATX {
		start := firstNonSpace(source, match.start, lineEndWithoutNewline(source, match.start))
		for start < len(source) && source[start] == '#' {
			start++
		}
		for start < len(source) && (source[start] == ' ' || source[start] == '\t') {
			start++
		}
		return start, start
	}
	return match.start, lineEndWithoutNewline(source, match.start)
}

func headingAttributeSpan(source []byte, heading *ast.Heading, match *nodeRef) (int, int, bool) {
	_, contentEnd := headingTextSpan(source, heading, match)
	searchEnd := lineEndWithoutNewline(source, contentEnd)
	if opening := bytes.IndexByte(source[contentEnd:searchEnd], '{'); opening >= 0 {
		start := contentEnd + opening
		for start > contentEnd && (source[start-1] == ' ' || source[start-1] == '\t') {
			start--
		}
		if closing := bytes.IndexByte(source[start:searchEnd], '}'); closing >= 0 {
			return start, start + closing + 1, true
		}
	}
	return contentEnd, contentEnd, false
}

func serializeAttributes(attributes map[string]string) string {
	if len(attributes) == 0 {
		return ""
	}
	parts := make([]string, 0, len(attributes)+2)
	if id, ok := attributes["id"]; ok {
		parts = append(parts, "#"+id)
	}
	if classes, ok := attributes["class"]; ok {
		for class := range strings.FieldsSeq(classes) {
			parts = append(parts, "."+class)
		}
	}
	keys := make([]string, 0, len(attributes))
	for name := range attributes {
		if name != "id" && name != "class" {
			keys = append(keys, name)
		}
	}
	sort.Strings(keys)
	for _, name := range keys {
		value := attributes[name]
		if value == name {
			parts = append(parts, name)
		} else if value != "" && !strings.ContainsAny(value, " \t\r\n{}\"'") {
			parts = append(parts, name+"="+value)
		} else {
			parts = append(parts, name+`="`+escapeQuoted(value)+`"`)
		}
	}
	return " {" + strings.Join(parts, " ") + "}"
}

type fenceParts struct {
	character                        byte
	length                           int
	prefix                           string
	openMarkerStart, openMarkerEnd   int
	infoStart, infoEnd               int
	contentStart, contentEnd         int
	closeMarkerStart, closeMarkerEnd int
	outerEnd                         int
}

func scanFence(source []byte, start, limit int) (fenceParts, bool) {
	start = lineStart(source, start)
	openEnd := lineEndWithoutNewline(source, start)
	markerStart := firstNonSpace(source, start, openEnd)
	for markerStart < openEnd && source[markerStart] == '>' {
		markerStart++
		for markerStart < openEnd && source[markerStart] == ' ' {
			markerStart++
		}
	}
	if markerStart >= openEnd || source[markerStart] != '`' && source[markerStart] != '~' {
		return fenceParts{}, false
	}
	character := source[markerStart]
	markerEnd := markerStart
	for markerEnd < openEnd && source[markerEnd] == character {
		markerEnd++
	}
	if markerEnd-markerStart < 3 {
		return fenceParts{}, false
	}
	infoStart := markerEnd
	for infoStart < openEnd && (source[infoStart] == ' ' || source[infoStart] == '\t') {
		infoStart++
	}
	infoEnd := openEnd
	for infoEnd > infoStart && (source[infoEnd-1] == ' ' || source[infoEnd-1] == '\t') {
		infoEnd--
	}
	contentStart := lineEnd(source, start)
	position := contentStart
	for position < min(limit, len(source)) {
		end := lineEndWithoutNewline(source, position)
		candidate := firstNonSpace(source, position, end)
		for candidate < end && source[candidate] == '>' {
			candidate++
			for candidate < end && source[candidate] == ' ' {
				candidate++
			}
		}
		closeEnd := candidate
		for closeEnd < end && source[closeEnd] == character {
			closeEnd++
		}
		if closeEnd-candidate >= markerEnd-markerStart && len(bytes.TrimSpace(source[closeEnd:end])) == 0 {
			return fenceParts{
				character: character, length: markerEnd - markerStart, prefix: string(source[start:markerStart]),
				openMarkerStart: markerStart, openMarkerEnd: markerEnd, infoStart: infoStart, infoEnd: infoEnd,
				contentStart: contentStart, contentEnd: position,
				closeMarkerStart: candidate, closeMarkerEnd: closeEnd, outerEnd: lineEnd(source, position),
			}, true
		}
		position = lineEnd(source, position)
	}
	return fenceParts{
		character: character, length: markerEnd - markerStart, prefix: string(source[start:markerStart]),
		openMarkerStart: markerStart, openMarkerEnd: markerEnd, infoStart: infoStart, infoEnd: infoEnd,
		contentStart: contentStart, contentEnd: min(limit, len(source)),
		closeMarkerStart: -1, closeMarkerEnd: -1, outerEnd: min(limit, len(source)),
	}, true
}

type linkParts struct {
	inline                           bool
	end                              int
	labelStart, labelEnd             int
	destinationStart, destinationEnd int
	titleStart, titleEnd             int
	titleOuterStart, titleOuterEnd   int
	closeParen                       int
}

func scanLink(source []byte, start, limit int) (linkParts, bool) {
	opening := start
	if opening < limit && source[opening] == '!' {
		opening++
	}
	if opening >= limit || source[opening] != '[' {
		return linkParts{}, false
	}
	labelEnd := findClosing(source, opening+1, limit, '[', ']')
	if labelEnd < 0 {
		return linkParts{}, false
	}
	result := linkParts{labelStart: opening + 1, labelEnd: labelEnd, titleStart: -1, titleOuterStart: -1}
	position := labelEnd + 1
	if position >= limit || source[position] != '(' {
		if position < limit && source[position] == '[' {
			closing := findClosing(source, position+1, limit, '[', ']')
			if closing >= 0 {
				result.end = closing + 1
				return result, true
			}
		}
		result.end = labelEnd + 1
		return result, true
	}
	result.inline = true
	result.closeParen = findClosing(source, position+1, limit, '(', ')')
	if result.closeParen < 0 {
		return linkParts{}, false
	}
	result.end = result.closeParen + 1
	position++
	for position < result.closeParen && unicode.IsSpace(rune(source[position])) {
		position++
	}
	if position < result.closeParen && source[position] == '<' {
		position++
		result.destinationStart = position
		for position < result.closeParen && source[position] != '>' {
			position++
		}
		result.destinationEnd = position
		if position < result.closeParen {
			position++
		}
	} else {
		result.destinationStart = position
		depth := 0
		for position < result.closeParen {
			value := source[position]
			if value == '\\' {
				position += 2
				continue
			}
			if value == '(' {
				depth++
			} else if value == ')' && depth > 0 {
				depth--
			} else if depth == 0 && unicode.IsSpace(rune(value)) {
				break
			}
			position++
		}
		result.destinationEnd = position
	}
	for position < result.closeParen && unicode.IsSpace(rune(source[position])) {
		position++
	}
	if position < result.closeParen && (source[position] == '"' || source[position] == '\'' || source[position] == '(') {
		quote := source[position]
		closing := quote
		if quote == '(' {
			closing = ')'
		}
		outerStart := position
		position++
		result.titleStart = position
		for position < result.closeParen && source[position] != closing {
			if source[position] == '\\' {
				position++
			}
			position++
		}
		result.titleEnd = position
		if position < result.closeParen {
			position++
		}
		result.titleOuterStart = outerStart
		for result.titleOuterStart > result.destinationEnd && unicode.IsSpace(rune(source[result.titleOuterStart-1])) {
			result.titleOuterStart--
		}
		result.titleOuterEnd = position
	}
	return result, true
}

func findClosing(source []byte, start, limit int, opening, closing byte) int {
	depth := 0
	for position := start; position < min(limit, len(source)); position++ {
		if source[position] == '\\' {
			position++
			continue
		}
		switch source[position] {
		case opening:
			depth++
		case closing:
			if depth == 0 {
				return position
			}
			depth--
		}
	}
	return -1
}

func cellContentSpan(source []byte, cell *extast.TableCell) (int, int) {
	segments := cell.Source()
	if len(segments) > 0 {
		return segments[0].Start, segments[len(segments)-1].Stop
	}
	start := cell.Pos()
	end := lineEndWithoutNewline(source, start)
	if pipe := bytes.IndexByte(source[start:end], '|'); pipe >= 0 {
		end = start + pipe
	}
	for start < end && unicode.IsSpace(rune(source[start])) {
		start++
	}
	for end > start && unicode.IsSpace(rune(source[end-1])) {
		end--
	}
	return start, end
}

func taskMarker(source []byte, start, end int) (int, bool) {
	limit := min(lineEndWithoutNewline(source, start), end)
	for position := start; position+2 < limit; position++ {
		if source[position] == '[' && (source[position+1] == ' ' || source[position+1] == 'x' || source[position+1] == 'X') && source[position+2] == ']' {
			return position + 1, true
		}
	}
	return 0, false
}

func tableBodyStart(source []byte, start, end int) int {
	afterHeader := lineEnd(source, start)
	afterDelimiter := lineEnd(source, afterHeader)
	return min(afterDelimiter, end)
}

func tableRowValue(value any) ([]string, error) {
	values, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("table row value must be a list of strings, got %T", value)
	}
	result := make([]string, len(values))
	for index, item := range values {
		text, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("table row cell %d is %T, want string", index+1, item)
		}
		if strings.ContainsAny(text, "\r\n") {
			return nil, fmt.Errorf("table row cell %d must be single-line", index+1)
		}
		result[index] = text
	}
	return result, nil
}

func stringValue(value any, name string) (string, error) {
	text, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("%s value must be string, got %T", name, value)
	}
	return text, nil
}

func integerValue(value any) (int, bool) {
	switch value := value.(type) {
	case int:
		return value, true
	case int64:
		return int(value), int64(int(value)) == value
	case float64:
		return int(value), value == float64(int(value))
	case string:
		parsed, err := strconv.Atoi(value)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func isInlineNode(node ast.Node) bool {
	_, ok := node.(ast.InlineNode)
	return ok
}

// escapeTableCell escapes a literal cell value for embedding in a table row.
// Backslashes are escaped before pipes so a value that already contains a
// backslash survives the round trip instead of turning its neighbour into an
// escape sequence.
func escapeTableCell(value string) string {
	return strings.NewReplacer(`\`, `\\`, "|", `\|`).Replace(value)
}

func escapeQuoted(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

func prefixLines(value, prefix string) string {
	if prefix == "" || value == "" {
		return value
	}
	lines := strings.SplitAfter(value, "\n")
	var result strings.Builder
	for _, line := range lines {
		if line == "" {
			continue
		}
		result.WriteString(prefix)
		result.WriteString(line)
	}
	return result.String()
}

func longestFenceRun(value string, marker byte) int {
	longest := 0
	for _, line := range strings.Split(value, "\n") {
		trimmed := strings.TrimLeft(line, " \t>")
		count := 0
		for count < len(trimmed) && trimmed[count] == marker {
			count++
		}
		longest = max(longest, count)
	}
	return longest
}

func ensureLineTerminated(value, ending string) string {
	if value == "" || strings.HasSuffix(value, "\n") || strings.HasSuffix(value, "\r") {
		return value
	}
	if ending == "" {
		ending = "\n"
	}
	return value + ending
}

func blockInsertion(source []byte, offset int, value []byte, ending string) []byte {
	if len(value) == 0 {
		return value
	}
	result := append([]byte(nil), value...)
	if offset > 0 && source[offset-1] != '\n' {
		result = append([]byte(ending), result...)
	}
	if offset < len(source) && !bytes.HasSuffix(result, []byte("\n")) {
		result = append(result, ending...)
	}
	return result
}

func preferredLineEnding(source []byte) string {
	if bytes.Contains(source, []byte("\r\n")) {
		return "\r\n"
	}
	return "\n"
}

func normalizeLineEndings(value, ending string) string {
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	if ending == "\r\n" {
		value = strings.ReplaceAll(value, "\n", "\r\n")
	}
	return value
}

func lineEnding(line []byte) string {
	if bytes.HasSuffix(line, []byte("\r\n")) {
		return "\r\n"
	}
	if bytes.HasSuffix(line, []byte("\n")) {
		return "\n"
	}
	return ""
}

func lineStart(source []byte, offset int) int {
	offset = min(max(offset, 0), len(source))
	if offset > 0 && offset == len(source) && source[offset-1] == '\n' {
		return offset
	}
	if start := bytes.LastIndexByte(source[:offset], '\n'); start >= 0 {
		return start + 1
	}
	return 0
}

func lineEndWithoutNewline(source []byte, offset int) int {
	offset = min(max(offset, 0), len(source))
	if end := bytes.IndexByte(source[offset:], '\n'); end >= 0 {
		result := offset + end
		if result > offset && source[result-1] == '\r' {
			result--
		}
		return result
	}
	return len(source)
}

func lineEnd(source []byte, offset int) int {
	offset = min(max(offset, 0), len(source))
	if end := bytes.IndexByte(source[offset:], '\n'); end >= 0 {
		return offset + end + 1
	}
	return len(source)
}

func endOfCoveredLine(source []byte, stop int) int {
	stop = min(max(stop, 0), len(source))
	if stop > 0 && source[stop-1] == '\n' {
		return stop
	}
	return lineEnd(source, stop)
}

func firstNonSpace(source []byte, start, end int) int {
	for start < end && (source[start] == ' ' || source[start] == '\t') {
		start++
	}
	return start
}
