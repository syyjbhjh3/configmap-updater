package controller

import (
	"context"
	"reflect"
	"testing"

	"k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	opsv1alpha1 "github.com/syyjbhjh3/configmap-updater/api/v1alpha1"
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

func TestGetConfigMapWithNamespaceNotFoundReturnsEmptyConfigMap(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	r := &ConfigMapUpdaterReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
	}

	cm, exists, err := r.getConfigMapWithNamespace(context.Background(), "default", "missing-cm")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exists {
		t.Fatalf("expected missing configmap to be treated as not existing")
	}
	if cm == nil || cm.Data == nil || len(cm.Data) != 0 || cm.BinaryData == nil || len(cm.BinaryData) != 0 {
		t.Fatalf("expected empty configmap when target is not found: %#v", cm)
	}
}

func TestGetConfigMapWithNamespaceReturnsExistingConfigMap(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	existing := &v1.ConfigMap{
		TypeMeta:   metav1.TypeMeta{},
		ObjectMeta: metav1.ObjectMeta{Name: "example", Namespace: "default"},
		Data:       map[string]string{"A": "1"},
	}
	r := &ConfigMapUpdaterReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build(),
	}

	cm, exists, err := r.getConfigMapWithNamespace(context.Background(), "default", "example")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exists {
		t.Fatalf("expected configmap to be found")
	}
	if got := cm.Data["A"]; got != "1" {
		t.Fatalf("unexpected configmap data: %#v", got)
	}
}

func TestDecodeYAMLDocumentsFindConfigMap(t *testing.T) {
	raw := []byte(`apiVersion: v1
kind: ConfigMap
metadata:
  name: test-cm
  namespace: default
data:
  KEY1: VALUE1
---
kind: Service
metadata:
  name: other
`)

	docs, err := decodeYAMLDocuments(raw)
	if err != nil {
		t.Fatalf("decode yaml documents failed: %v", err)
	}

	node, found := findConfigMapNode(docs, "test-cm", "default")
	if !found {
		t.Fatalf("expected to find target configmap in yaml documents")
	}

	data, binary := extractConfigMapData(node)
	if data["KEY1"] != "VALUE1" {
		t.Fatalf("unexpected configmap data: %#v", data)
	}
	if len(binary) != 0 {
		t.Fatalf("unexpected binary data parsed: %#v", binary)
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

func TestMergeSourceAndIgnoredKeys(t *testing.T) {
	sourceData := map[string]string{
		"KEEP_ME":   "target",
		"IGNORE_ME": "source",
	}
	sourceBinary := map[string][]byte{
		"BIN_KEEP":   []byte("source-bin"),
		"BIN_IGNORE": []byte("source-bin-ignore"),
	}
	targetData := map[string]string{
		"IGNORE_ME": "target-ignore",
		"OLD":       "legacy",
	}
	targetBinary := map[string][]byte{
		"BIN_IGNORE": []byte("target-bin-ignore"),
	}
	ignore := map[string]struct{}{
		"IGNORE_ME":  {},
		"BIN_IGNORE": {},
	}

	nextData, nextBinary := mergeSourceAndIgnoredKeys(sourceData, sourceBinary, targetData, targetBinary, ignore)

	if nextData["KEEP_ME"] != "target" {
		t.Fatalf("unexpected kept data key value: %#v", nextData)
	}
	if nextData["IGNORE_ME"] != "target-ignore" {
		t.Fatalf("ignored key should keep target value: %#v", nextData)
	}
	if _, ok := nextData["OLD"]; ok {
		t.Fatalf("data key not present in source should be removed: %#v", nextData)
	}
	if string(nextBinary["BIN_KEEP"]) != "source-bin" {
		t.Fatalf("expected source binary kept: %#v", nextBinary)
	}
	if string(nextBinary["BIN_IGNORE"]) != "target-bin-ignore" {
		t.Fatalf("ignored binary key should keep target value: %#v", nextBinary)
	}
}

func TestMapEqualityHelpers(t *testing.T) {
	left := map[string]string{"A": "1", "B": "2"}
	right := map[string]string{"B": "2", "A": "1"}
	if !mapsStringEqual(left, right) {
		t.Fatal("expected mapsStringEqual to treat same maps as equal")
	}

	leftBin := map[string][]byte{"A": []byte("alpha"), "B": []byte("beta")}
	rightBin := map[string][]byte{"A": []byte("alpha"), "B": []byte("beta")}
	if !mapsByteSliceMapEqual(leftBin, rightBin) {
		t.Fatal("expected mapsByteSliceMapEqual to treat same maps as equal")
	}

	right["A"] = "changed"
	if mapsStringEqual(left, right) {
		t.Fatal("expected mapsStringEqual to detect value difference")
	}
}

func TestSyncTargetConfigMapFromSourceUpdateKeepsIgnoredKeys(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := v1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}

	updater := &opsv1alpha1.ConfigMapUpdater{
		Spec: opsv1alpha1.ConfigMapUpdaterSpec{
			Target: opsv1alpha1.ConfigMapRef{
				Namespace: "default",
				Name:      "target-cm",
			},
		},
	}
	existing := &v1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "target-cm",
		},
		Data: map[string]string{
			"KEEP":   "old",
			"IGNORE": "cluster-value",
		},
	}
	r := &ConfigMapUpdaterReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(existing).Build(),
	}

	changed, err := r.syncTargetConfigMapFromSource(
		context.Background(),
		updater,
		map[string]string{"KEEP": "new", "IGNORE": "source-ignore"},
		nil,
		existing,
		true,
		map[string]struct{}{"IGNORE": {}},
	)
	if err != nil {
		t.Fatalf("sync target failed: %v", err)
	}
	if !changed {
		t.Fatal("expected target to be updated")
	}

	got := &v1.ConfigMap{}
	if err := r.Get(context.Background(), client.ObjectKeyFromObject(existing), got); err != nil {
		t.Fatalf("get updated target failed: %v", err)
	}
	expected := map[string]string{
		"KEEP":   "new",
		"IGNORE": "cluster-value",
	}
	if !reflect.DeepEqual(got.Data, expected) {
		t.Fatalf("unexpected target data: %#v", got.Data)
	}
}
