package markdownedit

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark/v2/ast"
	"github.com/yuin/goldmark/v2/extension"
	extast "github.com/yuin/goldmark/v2/extension/ast"
	"github.com/yuin/goldmark/v2/parser"
)

var markdownParser = parser.New(
	parser.WithAttribute(),
	parser.WithExtensions(extension.GFMParser),
)

type document struct {
	source  []byte
	root    ast.Node
	nodes   []*nodeRef
	byNode  map[ast.Node]*nodeRef
	lines   []int
	indexes map[ast.Node]int
	anchors map[*Selector]anchorResult
}

type anchorResult struct {
	match *nodeRef
	err   error
}

type nodeRef struct {
	node      ast.Node
	source    []byte
	kind      string
	path      string
	start     int
	end       int
	line      int
	column    int
	text      string
	textReady bool
	attrs     map[string]string
	level     int
	language  string
	info      string
	dest      string
	title     string
	task      bool
	checked   bool
	ordered   bool
	header    bool
	row       int
	columnNo  int
	alignment string
}

func parseDocument(source []byte) (*document, error) {
	root := markdownParser.Parse(source)
	if root == nil {
		return nil, fmt.Errorf("Goldmark returned an empty document")
	}
	doc := &document{
		source: source, root: root, byNode: make(map[ast.Node]*nodeRef),
		indexes: make(map[ast.Node]int), anchors: make(map[*Selector]anchorResult),
	}
	doc.lines = lineStarts(source)
	if err := ast.Walk(root, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		position := 0
		for child := node.FirstChild(); child != nil; child = child.NextSibling() {
			doc.indexes[child] = position
			position++
		}
		kind := markdownKind(node)
		if kind == "" {
			return ast.WalkContinue, nil
		}
		start, end := nodeSpan(source, node)
		if start < 0 || end < start || end > len(source) {
			return ast.WalkContinue, nil
		}
		line, column := doc.position(start)
		ref := &nodeRef{
			node: node, source: source, kind: kind, path: doc.nodePath(node),
			start: start, end: end, line: line, column: column,
		}
		doc.decorate(ref)
		doc.nodes = append(doc.nodes, ref)
		doc.byNode[node] = ref
		return ast.WalkContinue, nil
	}); err != nil {
		return nil, fmt.Errorf("walking Markdown AST: %w", err)
	}
	sort.SliceStable(doc.nodes, func(i, j int) bool {
		if doc.nodes[i].start != doc.nodes[j].start {
			return doc.nodes[i].start < doc.nodes[j].start
		}
		return doc.nodes[i].path < doc.nodes[j].path
	})
	return doc, nil
}

func markdownKind(node ast.Node) string {
	switch node.(type) {
	case *ast.Document:
		return "document"
	case *ast.Heading:
		return "heading"
	case *ast.Paragraph:
		return "paragraph"
	case *ast.Blockquote:
		return "blockquote"
	case *ast.CodeBlock:
		return "code_block"
	case *ast.Link, *ast.AutoLink:
		return "link"
	case *ast.Image:
		return "image"
	case *ast.List:
		return "list"
	case *ast.ListItem:
		return "list_item"
	case *extast.Table:
		return "table"
	case *extast.TableHeader, *extast.TableRow:
		return "table_row"
	case *extast.TableCell:
		return "table_cell"
	default:
		return ""
	}
}

func (d *document) decorate(ref *nodeRef) {
	switch node := ref.node.(type) {
	case *ast.Heading:
		ref.level = node.Level
		ref.attrs = attributes(node, d.source)
	case *ast.CodeBlock:
		ref.info = node.Info.Value(d.source)
		ref.language, _ = node.Language(d.source)
		ref.setText(node.Value.Str(d.source))
	case *ast.Link:
		ref.dest = node.Destination.Value(d.source)
		ref.title = node.Title.Value(d.source)
	case *ast.Image:
		ref.dest = node.Destination.Value(d.source)
		ref.title = node.Title.Value(d.source)
	case *ast.AutoLink:
		ref.dest = node.Destination.Value(d.source)
		ref.setText(node.Label.Value(d.source))
	case *ast.List:
		ref.ordered = node.IsOrdered()
	case *ast.ListItem:
		ref.task = extension.IsTask(node)
		if status, ok := extension.TaskStatusOf(node); ok {
			ref.checked = status == extension.TaskStatusCompleted
		}
	case *extast.TableCell:
		ref.alignment = node.Alignment.String()
		ref.header = node.Parent() != nil && node.Parent().Kind() == extast.KindTableHeader
		ref.columnNo = d.childIndex(node) + 1
		ref.row = d.tableRowNumber(node)
	case *extast.TableHeader:
		ref.header = true
		ref.row = 0
	case *extast.TableRow:
		ref.row = d.tableRowNumber(node)
	}
}

func attributes(node ast.Node, source []byte) map[string]string {
	result := make(map[string]string)
	for _, attribute := range node.Attributes() {
		if attribute.Name == "task-status" {
			continue
		}
		result[attribute.Name] = attribute.Value.Value(source)
	}
	return result
}

func (d *document) selectNodes(selector *Selector) ([]*nodeRef, error) {
	if selector.pattern == nil && selector.TextRegex != "" {
		return nil, fmt.Errorf("text_regex contains an unresolved template")
	}
	result := make([]*nodeRef, 0)
	for _, candidate := range d.nodes {
		matched, err := d.matches(candidate, selector)
		if err != nil {
			return nil, err
		}
		if matched {
			result = append(result, candidate)
		}
	}
	return result, nil
}

func (d *document) resolveAnchor(selector *Selector, name string) (*nodeRef, error) {
	if cached, ok := d.anchors[selector]; ok {
		if cached.err != nil {
			return nil, fmt.Errorf("%s anchor: %w", name, cached.err)
		}
		return cached.match, nil
	}
	matches, err := d.selectNodes(selector)
	if err != nil {
		d.anchors[selector] = anchorResult{err: err}
		return nil, fmt.Errorf("%s anchor: %w", name, err)
	}
	if selector.Occurrence > 0 {
		if selector.Occurrence > len(matches) {
			matches = nil
		} else {
			matches = matches[selector.Occurrence-1 : selector.Occurrence]
		}
	}
	if len(matches) != 1 {
		err := fmt.Errorf("found %d matches, want exactly one", len(matches))
		d.anchors[selector] = anchorResult{err: err}
		return nil, fmt.Errorf("%s anchor: %w", name, err)
	}
	d.anchors[selector] = anchorResult{match: matches[0]}
	return matches[0], nil
}

func (d *document) matches(candidate *nodeRef, selector *Selector) (bool, error) {
	if candidate.kind != selector.Kind {
		return false, nil
	}
	if selector.Text != "" && candidate.content() != selector.Text ||
		selector.pattern != nil && !selector.pattern.MatchString(candidate.content()) {
		return false, nil
	}
	if selector.Level != 0 && candidate.level != selector.Level ||
		selector.ID != "" && candidate.attrs["id"] != selector.ID ||
		selector.Language != "" && candidate.language != selector.Language ||
		selector.Destination != "" && candidate.dest != selector.Destination ||
		selector.Source != "" && candidate.dest != selector.Source ||
		selector.Title != "" && candidate.title != selector.Title {
		return false, nil
	}
	for name, value := range selector.Attributes {
		if candidate.attrs[name] != value {
			return false, nil
		}
	}
	if selector.Task != nil && candidate.task != *selector.Task ||
		selector.Checked != nil && candidate.checked != *selector.Checked ||
		selector.Ordered != nil && candidate.ordered != *selector.Ordered ||
		selector.Header != nil && candidate.header != *selector.Header {
		return false, nil
	}
	if len(selector.Headers) > 0 {
		table := tableAncestor(candidate.node)
		if table == nil || !equalStrings(tableHeaders(table, d.source), selector.Headers) {
			return false, nil
		}
	}
	if selector.Column != nil && !d.matchesColumn(candidate, selector.Column) {
		return false, nil
	}
	if selector.Parent != nil {
		parent := d.byNode[candidate.node.Parent()]
		if parent == nil {
			return false, nil
		}
		matched, err := d.matches(parent, selector.Parent)
		if err != nil || !matched {
			return false, err
		}
	}
	if selector.Ancestor != nil {
		matched, err := d.hasAncestor(candidate.node, selector.Ancestor)
		if err != nil || !matched {
			return false, err
		}
	}
	if selector.Contains != nil {
		matched, err := d.hasDescendant(candidate.node, selector.Contains)
		if err != nil || !matched {
			return false, err
		}
	}
	if selector.Before != nil {
		anchor, err := d.resolveAnchor(selector.Before, "before")
		if err != nil {
			return false, err
		}
		if candidate.end > anchor.start {
			return false, nil
		}
	}
	if selector.After != nil {
		anchor, err := d.resolveAnchor(selector.After, "after")
		if err != nil {
			return false, err
		}
		if candidate.start < anchor.end {
			return false, nil
		}
	}
	return true, nil
}

func (d *document) hasAncestor(node ast.Node, selector *Selector) (bool, error) {
	for parent := node.Parent(); parent != nil; parent = parent.Parent() {
		if ref := d.byNode[parent]; ref != nil {
			matched, err := d.matches(ref, selector)
			if err != nil {
				return false, err
			}
			if matched {
				return true, nil
			}
		}
	}
	return false, nil
}

func (d *document) hasDescendant(node ast.Node, selector *Selector) (bool, error) {
	found := false
	err := ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering || child == node {
			return ast.WalkContinue, nil
		}
		if ref := d.byNode[child]; ref != nil {
			matched, err := d.matches(ref, selector)
			if err != nil {
				return ast.WalkStop, err
			}
			if matched {
				found = true
				return ast.WalkStop, nil
			}
		}
		return ast.WalkContinue, nil
	})
	return found, err
}

func (d *document) matchesColumn(candidate *nodeRef, column any) bool {
	if candidate.kind != "table_cell" {
		return false
	}
	switch value := column.(type) {
	case int:
		return candidate.columnNo == value
	case string:
		table := tableAncestor(candidate.node)
		if table == nil {
			return false
		}
		headers := tableHeaders(table, d.source)
		matched := 0
		columnNo := 0
		for index, header := range headers {
			if header == value {
				matched++
				columnNo = index + 1
			}
		}
		return matched == 1 && candidate.columnNo == columnNo
	default:
		return false
	}
}

// content returns the node's flattened text, computed on first use so that
// indexing a document does not materialise text for every node.
func (n *nodeRef) content() string {
	if !n.textReady {
		n.text = nodeText(n.node, n.source)
		n.textReady = true
	}
	return n.text
}

func (n *nodeRef) setText(text string) {
	n.text, n.textReady = text, true
}

// rawText slices the node's original source on demand; eagerly copying it for
// every node duplicates the whole document once per nesting level.
func (n *nodeRef) rawText() string { return string(n.source[n.start:n.end]) }

func (n *nodeRef) descriptor() map[string]any {
	result := map[string]any{
		"kind": n.kind, "text": n.content(), "raw": n.rawText(), "path": n.path,
		"line": n.line, "column": n.column,
	}
	if n.level != 0 {
		result["level"] = n.level
	}
	if len(n.attrs) > 0 {
		attrs := make(map[string]any, len(n.attrs))
		for name, value := range n.attrs {
			attrs[name] = value
		}
		result["attributes"] = attrs
	}
	if n.kind == "code_block" {
		result["language"], result["info"] = n.language, n.info
	}
	if n.kind == "link" || n.kind == "image" {
		result["destination"], result["title"] = n.dest, n.title
	}
	if n.kind == "list" {
		result["ordered"] = n.ordered
	}
	if n.kind == "list_item" {
		result["task"], result["checked"] = n.task, n.checked
	}
	if n.kind == "table_row" || n.kind == "table_cell" {
		result["row"], result["header"] = n.row, n.header
	}
	if n.kind == "table_cell" {
		result["column"], result["alignment"] = n.columnNo, n.alignment
	}
	return result
}

func nodeText(node ast.Node, source []byte) string {
	var buffer strings.Builder
	_ = ast.Walk(node, func(child ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch value := child.(type) {
		case *ast.Text:
			buffer.WriteString(value.Value.Value(source))
			if value.SoftLineBreak() || value.HardLineBreak() {
				buffer.WriteByte('\n')
			}
		case *ast.CodeSpan:
			buffer.WriteString(value.Value.Value(source))
		case *ast.AutoLink:
			buffer.WriteString(value.Label.Value(source))
		case *ast.CodeBlock:
			buffer.WriteString(value.Value.Str(source))
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return strings.TrimSpace(buffer.String())
}

func tableAncestor(node ast.Node) *extast.Table {
	for current := node; current != nil; current = current.Parent() {
		if table, ok := current.(*extast.Table); ok {
			return table
		}
	}
	return nil
}

func tableHeaders(table *extast.Table, source []byte) []string {
	header, ok := table.FirstChild().(*extast.TableHeader)
	if !ok {
		return nil
	}
	result := make([]string, 0, header.ChildCount())
	for cell := header.FirstChild(); cell != nil; cell = cell.NextSibling() {
		result = append(result, nodeText(cell, source))
	}
	return result
}

func (d *document) tableRowNumber(node ast.Node) int {
	row := node
	if _, ok := node.(*extast.TableCell); ok {
		row = node.Parent()
	}
	if row == nil || row.Kind() == extast.KindTableHeader {
		return 0
	}
	return d.childIndex(row) + 1
}

// childIndex reports the position of node among its siblings. Positions are
// memoised while the AST is walked so that indexing a document stays linear.
func (d *document) childIndex(node ast.Node) int { return d.indexes[node] }

func (d *document) nodePath(node ast.Node) string {
	if node.Parent() == nil {
		return "/"
	}
	parts := make([]string, 0, 8)
	for current := node; current.Parent() != nil; current = current.Parent() {
		parts = append(parts, strconv.Itoa(d.childIndex(current)))
	}
	for left, right := 0, len(parts)-1; left < right; left, right = left+1, right-1 {
		parts[left], parts[right] = parts[right], parts[left]
	}
	return "/" + strings.Join(parts, "/")
}

func lineStarts(source []byte) []int {
	starts := []int{0}
	for index, value := range source {
		if value == '\n' && index+1 < len(source) {
			starts = append(starts, index+1)
		}
	}
	return starts
}

func (d *document) position(offset int) (int, int) {
	line := sort.Search(len(d.lines), func(index int) bool { return d.lines[index] > offset })
	if line == 0 {
		return 1, offset + 1
	}
	start := d.lines[line-1]
	return line, utf8.RuneCount(d.source[start:offset]) + 1
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func matchLocations(matches []*nodeRef) string {
	locations := make([]string, len(matches))
	for index, match := range matches {
		locations[index] = fmt.Sprintf("%d:%d", match.line, match.column)
	}
	return strings.Join(locations, ", ")
}
