package transform

import (
	"encoding/json"
	"strconv"
	"testing"

	"github.com/up2jj/wuko/step"
)

// benchmarkItems builds records shaped like decoded JSON: numbers arrive as json.Number.
func benchmarkItems(count int) []any {
	regions := []string{"eu", "us", "ap"}
	items := make([]any, count)
	for index := range items {
		items[index] = map[string]any{
			"id":      json.Number(strconv.Itoa(index)),
			"name":    "service-" + strconv.Itoa(index%5000),
			"enabled": index%3 != 0,
			"region":  regions[index%len(regions)],
			"tags":    []any{"a", "b", "c"},
			"score":   json.Number("0." + strconv.Itoa(index%1000)),
		}
	}
	return items
}

func benchmarkPipeline(b *testing.B, operations []any) {
	runner, err := New(map[string]any{"from": "vars.items", "operations": operations})
	if err != nil {
		b.Fatal(err)
	}
	request := step.Request{Vars: map[string]any{"items": benchmarkItems(100_000)}}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := runner.Run(b.Context(), request); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPipelineFilterMapUniqueSortTake(b *testing.B) {
	benchmarkPipeline(b, []any{
		map[string]any{"filter": "item.enabled"},
		map[string]any{"map": "{name: item.name, region: item.region, score: item.score}"},
		map[string]any{"unique_by": "item.name"},
		map[string]any{"sort_by": "item.name"},
		map[string]any{"take": 3},
	})
}

func BenchmarkPipelineMapTake(b *testing.B) {
	benchmarkPipeline(b, []any{
		map[string]any{"map": "item.name"},
		map[string]any{"take": 10},
	})
}

func BenchmarkPipelineFilterCount(b *testing.B) {
	benchmarkPipeline(b, []any{
		map[string]any{"filter": "item.region == 'eu'"},
		"count",
	})
}

func BenchmarkNormalizeResult(b *testing.B) {
	items := benchmarkItems(100_000)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := normalizeResult(items); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkNormalizeResultViaJSON(b *testing.B) {
	items := benchmarkItems(100_000)
	b.ReportAllocs()
	for b.Loop() {
		if _, err := referenceNormalize(items); err != nil {
			b.Fatal(err)
		}
	}
}
