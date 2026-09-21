package service

import (
	"strings"
	"testing"
	"time"

	"messagecatalog/internal/store"
)

func setupDemo(t *testing.T) *testEnv {
	e := newEnv(t, time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC))
	e.imp(t, "imp-zh", "zh", roleBaseline, map[string]string{
		"greeting": "你好 {name}",
		"items":    "{n, plural, one {# 项} other {# 项}}",
		"rich":     "<b>{name}</b>",
	})
	e.imp(t, "imp-ja", "ja", roleTarget, map[string]string{
		"greeting": "こんにちは {name}",
		// items missing, rich fine
		"rich":     "<b>{name}</b>",
		"ja.extra": "追加",
	})
	e.imp(t, "imp-fr", "fr", roleTarget, map[string]string{
		"greeting": "Bonjour {count}", // placeholder set mismatch
		"items":    "{n, plural, other {#}}",
		"rich":     "<i>{name}</i>",
	})
	return e
}

func TestValidationIssuesAndExemptionExpiry(t *testing.T) {
	e := setupDemo(t)
	v, err := e.svc.ValidateTarget("ja")
	if err != nil {
		t.Fatal(err)
	}
	if v.OpenCount == 0 {
		t.Fatal("expected open issues")
	}
	// Find missing items issue.
	var issueKey string
	for _, i := range v.Issues {
		if i.Type == "missing_key" && i.Key == "items" {
			issueKey = i.IssueKey
		}
	}
	if issueKey == "" {
		t.Fatalf("missing items issue not found: %+v", v.Issues)
	}
	exp := e.svc.now().Add(24 * time.Hour)
	res, replay, err := e.svc.AddExemption("ex-op", ExemptionRequest{
		ValidationID: v.ID, IssueKey: issueKey, Reason: "translated in next sprint",
		ExpiresAt: exp, ExpectedStateSeq: e.svc.Snapshot().Seq,
	})
	if err != nil || replay {
		t.Fatalf("add exemption: %v replay=%v", err, replay)
	}
	v2, err := e.svc.ValidateTarget("ja")
	if err != nil {
		t.Fatal(err)
	}
	if v2.OpenCount != v.OpenCount-1 || v2.ExemptCount != 1 {
		t.Fatalf("counts after exemption: open=%d exempt=%d", v2.OpenCount, v2.ExemptCount)
	}
	// Advance clock beyond deadline.
	e.svc.Clock.(*ControlledClock).Add(48 * time.Hour)
	v3, err := e.svc.ValidateTarget("ja")
	if err != nil {
		t.Fatal(err)
	}
	if v3.ExemptCount != 0 || v3.ExpiredCount != 1 || v3.OpenCount != v.OpenCount {
		t.Fatalf("expired exemption counts: open=%d exempt=%d expired=%d", v3.OpenCount, v3.ExemptCount, v3.ExpiredCount)
	}
	// Re-adding with same op replays, no extension.
	res2, _, err := e.svc.AddExemption("ex-op", ExemptionRequest{
		ValidationID: v.ID, IssueKey: issueKey, Reason: "translated in next sprint",
		ExpiresAt: exp, ExpectedStateSeq: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res2.Exemption.ID != res.Exemption.ID {
		t.Fatal("idempotent exemption created a new record")
	}
}

func TestExemptionStaleStateSeqConflict(t *testing.T) {
	e := setupDemo(t)
	v, _ := e.svc.ValidateTarget("ja")
	var issueKey string
	for _, i := range v.Issues {
		if i.Type == "missing_key" {
			issueKey = i.IssueKey
		}
	}
	_, _, err := e.svc.AddExemption("ex-stale", ExemptionRequest{
		ValidationID: v.ID, IssueKey: issueKey, Reason: "x",
		ExpiresAt: e.svc.now().Add(time.Hour), ExpectedStateSeq: v.StateSeq - 1,
	})
	if err == nil || !isConflict(err) {
		t.Fatalf("expected stale seq conflict, got %v", err)
	}
}

func TestBatchFixPartialSuccessAndRetry(t *testing.T) {
	e := setupDemo(t)
	// Set expected fr current to a stale fingerprint to force conflict; ja clean.
	frSnap := e.svc.Snapshot()
	var frFP string
	for _, l := range frSnap.State.Versions {
		if l.Language == "fr" {
			frFP = l.Fingerprint
		}
	}
	res, replay, err := e.svc.BatchFix("batch-X", BatchFixRequest{
		Reason: "sync", Languages: []string{"ja", "fr"},
		ExpectedVersions: map[string]string{"fr": "sha256:deadbeefdeadbeef"},
	})
	if err != nil || replay {
		t.Fatalf("batch: %v replay=%v", err, replay)
	}
	byLang := map[string]string{}
	ver := map[string]string{}
	for _, r := range res.Results {
		byLang[r.Language] = r.Status
		ver[r.Language] = r.VersionID
	}
	if byLang["ja"] != "applied" {
		t.Fatalf("ja should apply: %+v", res.Results)
	}
	if byLang["fr"] != "conflict" || !strings.Contains(resultReason(res.Results, "fr"), "version conflict") {
		t.Fatalf("fr should conflict: %+v", res.Results)
	}
	jaAppliedVersion := ver["ja"]

	// Retry same op with same request: replay, no new versions.
	res2, replay2, err := e.svc.BatchFix("batch-X", BatchFixRequest{
		Reason: "sync", Languages: []string{"ja", "fr"},
		ExpectedVersions: map[string]string{"fr": "sha256:deadbeefdeadbeef"},
	})
	if err != nil || !replay2 {
		t.Fatalf("retry should replay: %v replay=%v", err, replay2)
	}
	if len(e.svc.Batches()) != 1 {
		t.Fatalf("replay created another batch record: %d", len(e.svc.Batches()))
	}
	// ja remains applied at same version even after retry.
	for _, r := range res2.Results {
		if r.Language == "ja" && r.VersionID != jaAppliedVersion {
			t.Fatalf("ja version changed on retry: %s", r.VersionID)
		}
	}

	// New op with correct fr fingerprint -> fr applies, ja is now skipped (up to date).
	res3, _, err := e.svc.BatchFix("batch-Y", BatchFixRequest{
		Reason: "sync", Languages: []string{"ja", "fr"},
		ExpectedVersions: map[string]string{"fr": frFP},
	})
	if err != nil {
		t.Fatal(err)
	}
	st3 := map[string]string{}
	for _, r := range res3.Results {
		st3[r.Language] = r.Status
	}
	if st3["ja"] != "skipped" {
		t.Fatalf("ja should be skipped as up-to-date: %+v", res3.Results)
	}
	if st3["fr"] != "applied" {
		t.Fatalf("fr should apply on retry without conflict: %+v", res3.Results)
	}
}

func resultReason(rs []store.BatchLangResult, lang string) string {
	for _, r := range rs {
		if r.Language == lang {
			return r.Reason
		}
	}
	return ""
}

func TestBatchFixSameOpDifferentContentConflict(t *testing.T) {
	e := setupDemo(t)
	req := BatchFixRequest{Reason: "a", Languages: []string{"ja"}}
	if _, _, err := e.svc.BatchFix("op-same", req); err != nil {
		t.Fatal(err)
	}
	req2 := BatchFixRequest{Reason: "different", Languages: []string{"ja"}}
	_, _, err := e.svc.BatchFix("op-same", req2)
	if err == nil || !isConflict(err) {
		t.Fatalf("expected idempotency conflict, got %v", err)
	}
}
