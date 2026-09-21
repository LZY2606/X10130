package service

import (
	"bytes"
	"embed"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

//go:embed testdata_web
var testFS embed.FS

// remapFS serves testdata_web/index.html as web/index.html
type remapFS struct{ embed.FS }

func (remapFS) ReadFile(name string) ([]byte, error) {
	if name == "web/index.html" {
		return embed.FS.ReadFile(testFS, "testdata_web/index.html")
	}
	return embed.FS.ReadFile(testFS, name)
}

func httpEnv(t *testing.T) (*httptest.Server, *testEnv) {
	e := newEnv(t, time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC))
	srv := httptest.NewServer(e.svc.Handlers(remapFS{testFS}))
	t.Cleanup(srv.Close)
	e.server = srv
	return srv, e
}

func doUpload(t *testing.T, base, opKey, lang, role, filename, content string) map[string]any {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("language", lang)
	_ = mw.WriteField("role", role)
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = io.Copy(fw, strings.NewReader(content))
	mw.Close()
	req, _ := http.NewRequest("POST", base+"/api/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if opKey != "" {
		req.Header.Set("Idempotency-Key", opKey)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	if res.StatusCode != 200 {
		t.Fatalf("upload status %d: %+v", res.StatusCode, out)
	}
	return out
}

func TestHTTPUploadDownloadByteIdenticalAndIdempotent(t *testing.T) {
	srv, _ := httpEnv(t)
	raw := "{\n  \"a\": \"x\",\n  \"b\": \"y\\r\\nz\"\n}\n"
	out1 := doUpload(t, srv.URL, "http-op-1", "zh", "baseline", "orig.json", raw)
	out2 := doUpload(t, srv.URL, "http-op-1", "zh", "baseline", "orig.json", raw)
	r1 := out1["result"].(map[string]any)
	r2 := out2["result"].(map[string]any)
	if r1["versionId"] != r2["versionId"] {
		t.Fatal("http replay changed version")
	}
	if out2["replayed"] != true {
		t.Fatal("replayed flag missing on http retry")
	}
	url := r1["downloadUrl"].(string)
	res, err := http.Get(srv.URL + url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if string(b) != raw {
		t.Fatalf("downloaded raw differs: %q vs %q", string(b), raw)
	}
}

func TestHTTPSameKeyDifferentContentConflict(t *testing.T) {
	srv, _ := httpEnv(t)
	doUpload(t, srv.URL, "same", "zh", "baseline", "a.json", `{"a":"1"}`)
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("language", "zh")
	_ = mw.WriteField("role", "baseline")
	fw, _ := mw.CreateFormFile("file", "a.json")
	io.Copy(fw, strings.NewReader(`{"a":"2"}`))
	mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/import", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Idempotency-Key", "same")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("want 409, got %d", res.StatusCode)
	}
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	if out["conflict"] != true {
		t.Fatalf("expected conflict body: %+v", out)
	}
}

func TestHTTPUIShowsTitle(t *testing.T) {
	srv, _ := httpEnv(t)
	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(b), "消息目录校验") {
		t.Fatalf("UI missing title")
	}
}

func TestHTTPStaleRenameConflict(t *testing.T) {
	srv, e := httpEnv(t)
	doUpload(t, srv.URL, "b1", "zh", "baseline", "zh.json", `{"new":"x","keep":"y"}`)
	// Establish an old baseline version containing "old".
	doUpload(t, srv.URL, "b0", "zh", "baseline", "zh-old.json", `{"old":"x","keep":"y"}`)
	// then import current again so candidates include old->new possibilities.
	body := map[string]any{"from": "old", "to": "new", "expectedBaseVersion": "zh:STALE00000000"}
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", srv.URL+"/api/renames/confirm", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", "rn-http-1")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusConflict {
		t.Fatalf("want 409 got %d", res.StatusCode)
	}
	_ = e
}
