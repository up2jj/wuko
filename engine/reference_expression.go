package engine

import (
	"fmt"

	"github.com/expr-lang/expr/ast"
)

type expressionReferenceVisitor struct {
	scope   *referenceScope
	roots   map[string]struct{}
	locals  map[string]struct{}
	callees map[*ast.IdentifierNode]struct{}
	err     error
}

func (visitor *expressionReferenceVisitor) Visit(node *ast.Node) {
	if visitor.err != nil {
		return
	}
	if path := expressionStaticPath(*node); len(path) > 0 {
		if identifier, ok := (*node).(*ast.IdentifierNode); ok {
			if _, callee := visitor.callees[identifier]; callee {
				return
			}
		}
		if _, local := visitor.locals[path[0]]; local {
			return
		}
		if _, exists := visitor.scope.roots[path[0]]; !exists {
			if _, known := visitor.roots[path[0]]; !known {
				visitor.err = fmt.Errorf("data root %q is not available here", path[0])
				return
			}
		}
		visitor.err = visitor.scope.validate(path, visitor.roots)
		if visitor.err != nil {
			return
		}
	}
	switch typed := (*node).(type) {
	case *ast.CallNode:
		identifier, ok := typed.Callee.(*ast.IdentifierNode)
		if ok && (identifier.Value == "get" || identifier.Value == "hasKey") {
			visitor.validateConstantKeyCall(identifier.Value, typed.Arguments)
		}
	case *ast.BuiltinNode:
		if typed.Name == "get" || typed.Name == "hasKey" {
			visitor.validateConstantKeyCall(typed.Name, typed.Arguments)
		}
	}
}

type expressionReferenceMetadata struct {
	locals  map[string]struct{}
	callees map[*ast.IdentifierNode]struct{}
}

func (metadata *expressionReferenceMetadata) Visit(node *ast.Node) {
	switch typed := (*node).(type) {
	case *ast.VariableDeclaratorNode:
		metadata.locals[typed.Name] = struct{}{}
	case *ast.CallNode:
		if identifier, ok := typed.Callee.(*ast.IdentifierNode); ok {
			metadata.callees[identifier] = struct{}{}
		}
	}
}

func (visitor *expressionReferenceVisitor) validateConstantKeyCall(name string, arguments []ast.Node) {
	if len(arguments) != 2 {
		return
	}
	path := expressionStaticPath(arguments[0])
	if len(path) == 0 {
		return
	}
	if name == "hasKey" {
		// A presence test must not require the key it asks about.
		visitor.err = visitor.scope.validate(path, visitor.roots)
		return
	}
	key, ok := arguments[1].(*ast.StringNode)
	if !ok {
		return
	}
	visitor.err = visitor.scope.validate(append(path, key.Value), visitor.roots)
}

func expressionStaticPath(node ast.Node) []string {
	switch typed := node.(type) {
	case *ast.IdentifierNode:
		return []string{typed.Value}
	case *ast.MemberNode:
		path := expressionStaticPath(typed.Node)
		if len(path) == 0 {
			return nil
		}
		property, ok := typed.Property.(*ast.StringNode)
		if !ok {
			return path
		}
		return append(path, property.Value)
	case *ast.ChainNode:
		return expressionStaticPath(typed.Node)
	}
	return nil
}
