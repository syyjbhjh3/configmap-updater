package controller

import (
	"reflect"
	"testing"
)

func TestFilteredConfigMap(t *testing.T) {
	sourceData := map[string]string{"a": "1", "b": "2", "c": "3"}
	sourceBinary := map[string][]byte{"x": []byte("X"), "y": []byte("Y")}
	ignore := toIgnoreKeySet([]string{"b", "y"})

	filteredData, filteredBinary := filteredConfigMap(sourceData, sourceBinary, ignore)

	if _, ok := filteredData["b"]; ok {
		t.Fatal("expected ignored data key to be removed")
	}
	if _, ok := filteredBinary["y"]; ok {
		t.Fatal("expected ignored binary key to be removed")
	}
	if filteredData["a"] != "1" || filteredData["c"] != "3" {
		t.Fatalf("unexpected filtered data: %#v", filteredData)
	}
	if !reflect.DeepEqual(filteredBinary["x"], []byte("X")) {
		t.Fatalf("unexpected filtered binary: %#v", filteredBinary)
	}
}

func TestToIgnoreKeySetDeduplicatesAndPreservesKeys(t *testing.T) {
	ignore := toIgnoreKeySet([]string{"a", "b", "a", ""})

	if len(ignore) != 3 {
		t.Fatalf("expected 3 keys in set, got %d", len(ignore))
	}
	if _, ok := ignore[""]; !ok {
		t.Fatal("expected empty key to be preserved")
	}
	if _, ok := ignore["a"]; !ok {
		t.Fatal("expected key a to be preserved")
	}
}
