package controller

import (
	"testing"
	"time"
)

func TestHashConfigMapDeterministic(t *testing.T) {
	data := map[string]string{"a": "1", "b": "2"}
	binaryData := map[string][]byte{"x": []byte("y")}

	h1, err := hashConfigMap(data, binaryData)
	if err != nil {
		t.Fatalf("hashConfigMap returned error: %v", err)
	}
	h2, err := hashConfigMap(data, binaryData)
	if err != nil {
		t.Fatalf("hashConfigMap returned error: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("expected deterministic hash, got %s and %s", h1, h2)
	}
}

func TestWithJitterRange(t *testing.T) {
	base := 5 * time.Minute
	got := withJitter(base, "viola/viola-cm-updater")
	if got < base {
		t.Fatalf("expected jittered duration >= base, got %v", got)
	}
	if got > base+30*time.Second {
		t.Fatalf("expected jitter cap <= 30s, got %v", got-base)
	}
}

func TestWithJitterDeterministicByKey(t *testing.T) {
	base := 5 * time.Minute
	k := "viola/viola-cm-updater"
	a := withJitter(base, k)
	b := withJitter(base, k)
	if a != b {
		t.Fatalf("expected deterministic jitter for same key, got %v and %v", a, b)
	}
}
