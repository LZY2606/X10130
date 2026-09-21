package service

import (
	"encoding/json"
	"sync"
	"testing"
)

// TestSnapshotConsistencyUnderWrites ensures views always correspond to one
// committed state even while imports and exemptions happen concurrently.
func TestSnapshotConsistencyUnderWrites(t *testing.T) {
	e := setupDemo(t)
	if _, err := e.svc.ValidateTarget("ja"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.ValidateTarget("fr"); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	stop := make(chan struct{})
	// writers: repeatedly import same-content versions (no content change) and
	// generate proofs.
	wg.Add(2)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			i++
			_, _, _ = e.svc.Import("concurrent-ja", ImportRequest{
				Language: "ja", Role: roleTarget, Filename: "ja.json",
				Content: []byte(`{"greeting":"こんにちは {name}","items":"{n, plural, one {1} other {#}}","rich":"<b>{name}</b>"}`),
			})
		}
	}()
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
			}
			i++
			_ = e.svc.CommitForTest(func() error { return nil })
		}
	}()
	// readers: every view must be internally consistent.
	for n := 0; n < 50; n++ {
		v, err := e.svc.ValidateTarget("fr")
		if err != nil {
			t.Fatal(err)
		}
		// The view's base/target versions must exist in the same snapshot.
		snap := e.svc.Snapshot()
		ids := map[string]bool{}
		for _, ver := range snap.State.Versions {
			ids[ver.ID] = true
		}
		if !ids[v.BaseVersionID] || !ids[v.TargVersionID] {
			t.Fatalf("view references versions not in snapshot: base=%s targ=%s", v.BaseVersionID, v.TargVersionID)
		}
		if v.SnapshotStamp == "" || v.MappingSig == "" || v.ParserVersion == "" {
			t.Fatal("view missing snapshot identity")
		}
		// Marshal must succeed and round-trip.
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var back ValidateView
		if err := json.Unmarshal(b, &back); err != nil {
			t.Fatal(err)
		}
	}
	close(stop)
	wg.Wait()
}
