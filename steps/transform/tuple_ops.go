package transform

import (
	"context"
	"fmt"
)

func tupleOperation(ctx context.Context, request *evaluation, list []any, operation operation) (any, error) {
	switch operation.name {
	case "unzip", "unzip_with":
		rows := make([][]any, len(list))
		for index, item := range list {
			value := item
			if operation.name == "unzip_with" {
				var err error
				value, err = evaluateItem(ctx, request, operation, "expr", item, index)
				if err != nil {
					return nil, err
				}
			}
			row, err := toList(value)
			if err != nil {
				return nil, fmt.Errorf("item %d must be a tuple list: %w", index, err)
			}
			rows[index] = row
		}
		return unzipRows(rows)
	case "zip", "zip_with", "cross_join", "cross_join_with":
		others, err := resolveListOperands(request, operation)
		if err != nil {
			return nil, err
		}
		lists := append([][]any{list}, others...)
		if len(lists) < 2 || len(lists) > 9 {
			return nil, fmt.Errorf("tuple arity must be between 2 and 9")
		}
		var rows [][]any
		if operation.name == "zip" || operation.name == "zip_with" {
			rows = zipLists(lists)
		} else {
			rows, err = crossJoinLists(ctx, lists)
			if err != nil {
				return nil, err
			}
		}
		if operation.name == "zip" || operation.name == "cross_join" {
			result := make([]any, len(rows))
			for index := range rows {
				result[index] = rows[index]
			}
			return result, nil
		}
		result := make([]any, len(rows))
		for index, row := range rows {
			if err := checkContext(ctx); err != nil {
				return nil, err
			}
			value, err := operation.expressions["expr"].evaluate(request, map[string]any{"items": row, "index": index})
			if err != nil {
				return nil, fmt.Errorf("tuple %d: %w", index, err)
			}
			result[index] = value
		}
		return result, nil
	}
	return nil, fmt.Errorf("tuple operation is not implemented")
}

func zipLists(lists [][]any) [][]any {
	maximum := 0
	for _, list := range lists {
		maximum = max(maximum, len(list))
	}
	result := make([][]any, maximum)
	for index := range maximum {
		row := make([]any, len(lists))
		for listIndex, list := range lists {
			if index < len(list) {
				row[listIndex] = list[index]
			}
		}
		result[index] = row
	}
	return result
}

// crossJoinLists materializes the Cartesian product. The row count is the product of
// the input lengths, so it grows fast enough that a run has to stay cancellable while
// it builds -- three 250-element lists are already 15.6 million rows -- and the
// capacity has to be checked before it wraps a negative make.
func crossJoinLists(ctx context.Context, lists [][]any) ([][]any, error) {
	for _, list := range lists {
		if len(list) == 0 {
			return [][]any{}, nil
		}
	}
	result := [][]any{{}}
	for _, list := range lists {
		total := len(result) * len(list)
		if total/len(list) != len(result) {
			return nil, fmt.Errorf("cross join result is too large")
		}
		if err := checkContext(ctx); err != nil {
			return nil, err
		}
		next := make([][]any, 0, total)
		for _, prefix := range result {
			if err := checkContext(ctx); err != nil {
				return nil, err
			}
			for _, item := range list {
				row := make([]any, len(prefix), len(prefix)+1)
				copy(row, prefix)
				row = append(row, item)
				next = append(next, row)
			}
		}
		result = next
	}
	return result, nil
}

func unzipRows(rows [][]any) ([]any, error) {
	if len(rows) == 0 {
		return []any{}, nil
	}
	width := len(rows[0])
	if width < 2 || width > 9 {
		return nil, fmt.Errorf("tuple arity must be between 2 and 9")
	}
	columns := make([]any, width)
	for column := range width {
		columns[column] = make([]any, 0, len(rows))
	}
	for index, row := range rows {
		if len(row) != width {
			return nil, fmt.Errorf("item %d has tuple width %d; expected %d", index, len(row), width)
		}
		for column, value := range row {
			columns[column] = append(columns[column].([]any), value)
		}
	}
	return columns, nil
}
