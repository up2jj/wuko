package plugin

import (
	"encoding/json"
	"testing"

	"github.com/up2jj/wuko/step"
)

func TestPluginOutputSchemaDeclarationIsRecursiveAndOptional(t *testing.T) {
	var initialized initializeResult
	data := []byte(`{"protocol":"2","namespace":"acme","steps":[
		{"type":"acme.typed","outputs":{"fields":{"status":{},"metadata":{"fields":{"request_id":{}}},"items":{"items":{}}}}},
		{"type":"acme.open"}
	],"executors":[]}`)
	if err := json.Unmarshal(data, &initialized); err != nil {
		t.Fatal(err)
	}
	if initialized.Steps[1].Outputs != nil {
		t.Fatal("schema-less plugin step must remain open")
	}
	schema := initialized.Steps[0].Outputs.stepSchema()
	if schema.Kind != step.OutputObject || schema.Open {
		t.Fatalf("schema = %#v", schema)
	}
	if schema.Fields["metadata"].Kind != step.OutputObject || schema.Fields["metadata"].Open {
		t.Fatalf("metadata = %#v", schema.Fields["metadata"])
	}
	if schema.Fields["items"].Kind != step.OutputArray {
		t.Fatalf("items = %#v", schema.Fields["items"])
	}
}

func TestPluginOutputSchemaOpenWithoutFieldsIsOpenObject(t *testing.T) {
	var declaration outputSchemaDeclaration
	if err := json.Unmarshal([]byte(`{"open":true}`), &declaration); err != nil {
		t.Fatal(err)
	}
	if schema := declaration.stepSchema(); schema.Kind != step.OutputObject || !schema.Open {
		t.Fatalf("schema = %#v, want open object", schema)
	}
}
