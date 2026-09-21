package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"
	"time"

	"msgcatalog/internal/store"
)

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	st, err := store.Open(t.TempDir(), func() time.Time { return time.Unix(1000, 0) })
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(New(st, func() time.Time { return time.Unix(1000, 0) }).Routes())
	t.Cleanup(func() { srv.Close(); st.Close() })
	return srv, st
}

func upload(t *testing.T, url, op, lang, body string, baseline bool) map[string]any {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="c.json"`)
	h.Set("Content-Type", "application/json")
	fw, _ := mw.CreatePart(h)
	io.WriteString(fw, body)
	mw.WriteField("language", lang)
	if baseline {
		mw.WriteField("baseline", "true")
	}
	mw.Close()
	req, _ := http.NewRequest("POST", url+"/api/import?op="+op, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var j map[string]any
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatalf("import resp %d: %s", resp.StatusCode, b)
	}
	return j
}

func postJSON(t *testing.T, url, op, path string, v any) (int, map[string]any) {
	b, _ := json.Marshal(v)
	req, _ := http.NewRequest("POST", url+path+"?op="+op, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var j map[string]any
	_ = json.Unmarshal(data, &j)
	return resp.StatusCode, j
}

func getJSON(t *testing.T, url string) map[string]any {
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var j map[string]any
	b, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(b, &j); err != nil {
		t.Fatalf("get %s: %s", url, b)
	}
	return j
}

func TestIndexTitle(t *testing.T) {
	srv, _ := newTestServer(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "消息目录校验") {
		t.Fatal("index must contain title")
	}
}

func TestHTTPEndToEndIdempotencyAndConflict(t *testing.T) {
	srv, _ := newTestServer(t)
	en := `{"messages":[{"key":"a","text":"Hello {name}"},{"key":"b","text":"{n, plural, one {#} other {#s}}"}]}`
	ja := `{"messages":[{"key":"a","text":"こんにちは {name}"},{"key":"b","text":"{n, plural, other {#}}"}]}`
	r1 := upload(t, srv.URL, "en-op", "en", en, true)
	// Repeat identical operation key+content: no new seq.
	upload(t, srv.URL, "en-op", "en", en, true)
	st := getJSON(t, srv.URL+"/api/state")
	if int64(st["seq"].(float64)) != int64(r1["seq"].(float64)) {
		t.Fatal("same import op key must not add seq")
	}
	// Same op key, different content -> 409.
	buf := strings.NewReader("")
	_ = buf
	upload(t, srv.URL, "en-op", "en", `{"messages":[{"key":"a","text":"DIFFERENT"}]}`, true)
	// upload helper fatals on non-json; do raw to see 409:
	var b2 bytes.Buffer
	mw := multipart.NewWriter(&b2)
	mw.WriteField("language", "en")
	mw.WriteField("baseline", "true")
	fw, _ := mw.CreateFormFile("file", "c.json")
	io.WriteString(fw, `{"messages":[{"key":"a","text":"DIFFERENT"}]}`)
	mw.Close()
	req, _ := http.NewRequest("POST", srv.URL+"/api/import?op=en-op", &b2)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for same key different content, got %d", resp.StatusCode)
	}

	upload(t, srv.URL, "ja-op", "ja", ja, false)
	iv := getJSON(t, srv.URL+"/api/issues")
	if int64(iv["unexempted_count"].(float64)) == 0 {
		t.Fatal("expected unexempted issues (missing plural category)")
	}
	// Raw download byte-identical.
	state := getJSON(t, srv.URL+"/api/state")
	langs := state["languages"].([]any)
	enLang := langs[0].(map[string]any)
	if enLang["name"] != "en" {
		enLang = langs[1].(map[string]any)
	}
	imp := enLang["imports"].([]any)[0].(map[string]any)
	raw := imp["raw"].(map[string]any)
	sha := raw["blob_sha"].(string)
	rb, _ := http.Get(srv.URL + "/api/raw?sha=" + sha)
	data, _ := io.ReadAll(rb.Body)
	if string(data) != en {
		t.Fatal("downloaded raw must be byte-identical to upload")
	}

	// Key view resolves structures.
	kv := getJSON(t, srv.URL+"/api/key?key=b")
	if len(kv["languages"].([]any)) != 2 {
		t.Fatal("key view must include all languages")
	}

	// Proof determinism over HTTP.
	code, p1 := postJSON(t, srv.URL, "proof-op", "/api/proof", map[string]any{})
	if code != 200 {
		t.Fatalf("proof %d %v", code, p1)
	}
	_, p2 := postJSON(t, srv.URL, "proof-op", "/api/proof", map[string]any{})
	if p1["bytes"].(string) != p2["bytes"].(string) {
		t.Fatal("proof must be byte-identical across same op key")
	}
}
