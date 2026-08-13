package cmd

import "testing"

func TestApplyDirenvExport(t *testing.T) {
	environment := map[string]string{
		"CHANGED": "old",
		"KEPT":    "value",
		"REMOVED": "value",
	}
	if err := applyDirenvExport(environment, []byte(`{"CHANGED":"new","EMPTY":"","REMOVED":null}`)); err != nil {
		t.Fatal(err)
	}
	if environment["CHANGED"] != "new" || environment["EMPTY"] != "" || environment["KEPT"] != "value" {
		t.Fatalf("environment = %#v", environment)
	}
	if _, exists := environment["REMOVED"]; exists {
		t.Fatalf("removed variable is still present: %#v", environment)
	}
}

func TestApplyDirenvExportRejectsInvalidJSON(t *testing.T) {
	if err := applyDirenvExport(map[string]string{}, []byte(`not JSON`)); err == nil {
		t.Fatal("expected error")
	}
}

func TestApplyDirenvExportAcceptsEmptyOutput(t *testing.T) {
	environment := map[string]string{"KEPT": "value"}
	if err := applyDirenvExport(environment, nil); err != nil {
		t.Fatal(err)
	}
	if environment["KEPT"] != "value" {
		t.Fatalf("environment = %#v", environment)
	}
}
