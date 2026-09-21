package service

import (
	"messagecatalog/internal/store"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDbgPersist(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("dir=%s", dir)
	svc := New(st)
	svc.Clock = NewControlledClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	_, _, err = svc.Import("p1", ImportRequest{Language: "zh", Role: roleBaseline, Filename: "zh.json", Content: []byte(`{"a":"b"}`)})
	if err != nil {
		t.Fatal(err)
	}
	es, _ := os.ReadDir(filepath.Join(dir, "state"))
	for _, e := range es {
		t.Logf("file %s", e.Name())
	}
	st2, err := store.Open(dir)
	if err != nil {
		t.Fatal("reopen", err)
	}
	svc2 := New(st2)
	t.Logf("LANGS=%+v degraded=%v events=%+v", svc2.Languages(), st2.Degraded(), st2.RecoveryEvents())
}
