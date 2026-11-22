package library

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func validEntry() CatalogEntry {
	return CatalogEntry{ID: "demo", Title: "Demo", Artist: "Artist", DurationMs: 0,
		AudioFile: "demo.mp3", Source: "https://example.org/track", License: "CC0-1.0",
		LicenseURL: "https://creativecommons.org/publicdomain/zero/1.0/", Attribution: "Artist"}
}

func catalogJSON(t *testing.T, entries ...CatalogEntry) string {
	t.Helper()
	data, err := json.Marshal(catalogFile{Tracks: entries})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCatalogStructure(t *testing.T) {
	for _, input := range []string{"", "{", "null", "[]", "{}", `{"tracks":null}`, `{"tracks":{}}`,
		`{"tracks":[]} {}`, `{"tracks":[]} garbage`, `{"tracks":[],"extra":true}`,
		`{"tracks":[]}` + strings.Repeat(" ", 1<<20)} {
		t.Run(fmt.Sprintf("%.40q", input), func(t *testing.T) {
			if _, err := LoadCatalog(strings.NewReader(input), "https://music.example"); err == nil {
				t.Fatal("accepted invalid catalog")
			}
		})
	}
	catalog, err := LoadCatalog(strings.NewReader(`{"tracks":[]}`), "https://music.example")
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(catalog, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/tracks", nil))
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"tracks":[]}` {
		t.Fatalf("empty response: %d %s", w.Code, w.Body.String())
	}
}

func TestCatalogValidation(t *testing.T) {
	mutations := map[string]func(*CatalogEntry){
		"id": func(e *CatalogEntry) { e.ID = " " }, "title": func(e *CatalogEntry) { e.Title = "" },
		"artist": func(e *CatalogEntry) { e.Artist = "\t" }, "duration": func(e *CatalogEntry) { e.DurationMs = -1 },
		"source": func(e *CatalogEntry) { e.Source = "" }, "license": func(e *CatalogEntry) { e.License = "" },
		"license URL": func(e *CatalogEntry) { e.LicenseURL = "" }, "attribution": func(e *CatalogEntry) { e.Attribution = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			e := validEntry()
			mutate(&e)
			if _, err := LoadCatalog(strings.NewReader(catalogJSON(t, e)), "https://music.example"); err == nil {
				t.Fatal("accepted invalid entry")
			}
		})
	}
	for _, duplicateID := range []bool{false, true} {
		a, b := validEntry(), validEntry()
		if duplicateID {
			b.AudioFile = "other.mp3"
		} else {
			b.ID = "other"
		}
		if _, err := LoadCatalog(strings.NewReader(catalogJSON(t, a, b)), "https://music.example"); err == nil {
			t.Fatal("accepted duplicate ID or filename")
		}
	}
}

func TestCatalogPublicURL(t *testing.T) {
	input := catalogJSON(t, validEntry())
	for _, base := range []string{"", "music.example", "/relative", "//music.example", "ftp://music.example", "http:///audio", "http://", "https://user:pass@music.example", "https://music.example?x=1", "https://music.example?", "https://music.example#fragment", "https://music.example#", "https://music.example/prefix", "https://music.example/%2f", "https://music.example:bad", "https://music.example:0", "https://music.example:65536", "https://music.example:", "https://music.example\n" + "evil"} {
		t.Run(base, func(t *testing.T) {
			if _, err := LoadCatalog(strings.NewReader(input), base); err == nil {
				t.Fatal("accepted invalid base URL")
			}
		})
	}
	for _, base := range []string{"https://music.example", "http://192.0.2.10:8090/", "http://[::1]:8090"} {
		if _, err := LoadCatalog(strings.NewReader(input), base); err != nil {
			t.Errorf("%s: %v", base, err)
		}
	}
}

func TestFilenameSafety(t *testing.T) {
	for _, name := range []string{"", "../secret.mp3", `..\secret.mp3`, "/absolute.mp3", `C:\secret.mp3`, "file.mp3:stream.mp3", "evil\x00.mp3", "line\n.mp3", " leading.mp3", "trailing.mp3 ", "song.wav", "%2e%2e.mp3", "CON.mp3", "NUL.mp3", "COM1.mp3", "LPT¹.mp3", "what?.mp3", "star*.mp3"} {
		e := validEntry()
		e.AudioFile = name
		if _, err := LoadCatalog(strings.NewReader(catalogJSON(t, e)), "https://music.example"); err == nil {
			t.Errorf("accepted filename %q", name)
		}
	}
}

func TestEscapedAudioURLRoundTrip(t *testing.T) {
	e := validEntry()
	e.AudioFile = "Café #1 + (demo).mp3"
	catalog, err := LoadCatalog(strings.NewReader(catalogJSON(t, e)), "https://music.example/")
	if err != nil {
		t.Fatal(err)
	}
	tracks := catalog.Tracks()
	u, err := url.Parse(tracks[0].URL)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/audio/"+e.AudioFile || u.Fragment != "" || u.RawQuery != "" || strings.Contains(tracks[0].URL, " ") {
		t.Fatalf("unsafe URL: %s", tracks[0].URL)
	}
	tracks[0].Title = "mutated"
	if catalog.Tracks()[0].Title != e.Title {
		t.Fatal("Tracks exposes mutable catalog storage")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, e.AudioFile), []byte("escaped audio"), 0600); err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(catalog, root)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequest("GET", u.RequestURI(), nil))
	if w.Code != 200 || w.Body.String() != "escaped audio" {
		t.Fatalf("URL not reachable: %d %s", w.Code, w.Body.String())
	}
}

func TestHTTPMethodsAndErrors(t *testing.T) {
	server, root := testServer(t)
	for _, route := range []string{"/tracks", "/audio/track-001.mp3"} {
		for _, method := range []string{"POST", "PUT", "PATCH", "DELETE", "OPTIONS", "HEAD"} {
			if method == "HEAD" && strings.HasPrefix(route, "/audio/") {
				continue
			}
			w := httptest.NewRecorder()
			server.Handler().ServeHTTP(w, httptest.NewRequest(method, route, nil))
			wantAllow := "GET"
			if strings.HasPrefix(route, "/audio/") {
				wantAllow = "GET, HEAD"
			}
			if w.Code != 405 || w.Header().Get("Allow") != wantAllow {
				t.Errorf("%s %s: %d Allow=%s", method, route, w.Code, w.Header().Get("Allow"))
			}
			var body struct {
				Error struct {
					Code string `json:"code"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Error.Code == "" {
				t.Errorf("invalid error: %s", w.Body.String())
			}
		}
	}
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequest("HEAD", "/audio/track-001.mp3", nil))
	if w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "8" || w.Header().Get("Content-Type") != "audio/mpeg" {
		t.Fatalf("HEAD: %d %v %q", w.Code, w.Header(), w.Body.String())
	}
	if err := os.Remove(filepath.Join(root, "track-001.mp3")); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/audio/track-001.mp3", nil))
	if w.Code != 404 || strings.Contains(w.Body.String(), root) {
		t.Fatalf("missing asset: %d %s", w.Code, w.Body.String())
	}
	if err := os.Mkdir(filepath.Join(root, "track-001.mp3"), 0700); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/audio/track-001.mp3", nil))
	if w.Code != 404 {
		t.Fatalf("served directory: %d", w.Code)
	}
}

func TestEncodedTraversal(t *testing.T) {
	server, root := testServer(t)
	if err := os.WriteFile(filepath.Join(root, "not-listed.mp3"), []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/audio/../secret.mp3", "/audio/%2e%2e/secret.mp3", "/audio/..%2fsecret.mp3", "/audio/%2e%2e%5csecret.mp3", "/audio/%252e%252e%252fsecret.mp3", "/audio/%2fsecret.mp3", "/audio/%00.mp3", "/audio/C:%5csecret.mp3", "/audio//track-001.mp3", "/audio/track-001.mp3/extra", "/audio/not-listed.mp3"} {
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, httptest.NewRequest("GET", path, nil))
		want := 400
		if path == "/audio/not-listed.mp3" {
			want = 404
		}
		if w.Code != want {
			t.Errorf("%s: status %d, want %d", path, w.Code, want)
		}
	}
}

func TestAudioSymlinks(t *testing.T) {
	server, root := testServer(t)
	outside := filepath.Join(t.TempDir(), "secret.mp3")
	if err := os.WriteFile(outside, []byte("outside secret"), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "track-001.mp3")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink privilege unavailable: %v", err)
		}
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/audio/track-001.mp3", nil))
	if w.Code != 404 || strings.Contains(w.Body.String(), "outside secret") {
		t.Fatalf("symlink escaped root: %d %s", w.Code, w.Body.String())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "inside.mp3"), []byte("inside audio"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside.mp3", path); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	server.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/audio/track-001.mp3", nil))
	if w.Code != 200 || w.Body.String() != "inside audio" {
		t.Fatalf("confined symlink: %d %s", w.Code, w.Body.String())
	}
}

func TestConcurrentRequests(t *testing.T) {
	server, _ := testServer(t)
	ts := httptest.NewServer(server.Handler())
	defer ts.Close()
	client := ts.Client()
	client.Timeout = 5 * time.Second
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, path := range []string{"/tracks", "/audio/track-001.mp3", "/audio/missing.mp3"} {
				resp, err := client.Get(ts.URL + path)
				if err != nil {
					t.Error(err)
					return
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil {
					t.Error(err)
					return
				}
				want := 200
				if path == "/audio/missing.mp3" {
					want = 404
				}
				if resp.StatusCode != want {
					t.Errorf("%s: %d", path, resp.StatusCode)
				}
				if path == "/audio/track-001.mp3" && string(body) != "test-mp3" {
					t.Errorf("corrupt audio: %q", body)
				}
				if path == "/tracks" && !strings.Contains(string(body), `"id":"track-001"`) {
					t.Errorf("corrupt tracks: %s", body)
				}
			}
		}()
	}
	wg.Wait()
}

func TestNewServerRejectsInvalidConfig(t *testing.T) {
	if _, err := NewServer(nil, t.TempDir()); err == nil {
		t.Fatal("accepted nil catalog")
	}
	catalog, err := LoadCatalog(strings.NewReader(`{"tracks":[]}`), "https://music.example")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewServer(catalog, filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("accepted missing root")
	}
}

func TestBundledCatalogAudio(t *testing.T) {
	root := filepath.Join("..", "..", "assets", "music")
	f, err := os.Open(filepath.Join(root, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog(f, "http://192.0.2.10:8090")
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	server, err := NewServer(catalog, root)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if len(catalog.Tracks()) == 0 {
		t.Fatal("demo catalog is empty")
	}
	for _, track := range catalog.Tracks() {
		u, err := url.Parse(track.URL)
		if err != nil {
			t.Fatal(err)
		}
		entry, ok := catalog.EntryByFilename(strings.TrimPrefix(u.Path, "/audio/"))
		if !ok {
			t.Fatalf("catalog URL not allowlisted: %s", track.URL)
		}
		data, err := os.ReadFile(filepath.Join(root, entry.AudioFile))
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, httptest.NewRequest("GET", u.RequestURI(), nil))
		if w.Code != 200 || w.Header().Get("Content-Type") != "audio/mpeg" || w.Body.String() != string(data) || len(data) == 0 {
			t.Fatalf("demo audio unavailable: %s status=%d", track.ID, w.Code)
		}
	}
}
