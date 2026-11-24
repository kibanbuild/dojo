package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/kibanbuild/dojo/internal/catalog"
	"github.com/kibanbuild/dojo/internal/state"
)

func testBundle(t *testing.T, tree fs.FS, id string) *catalog.Bundle {
	t.Helper()
	b, err := catalog.BuildBundle(tree, id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// serveBundle answers the bundle route with the JSON answer and any other path with the archive.
func serveBundle(w http.ResponseWriter, r *http.Request, b *catalog.Bundle, version, dl string) {
	if !strings.HasSuffix(r.URL.Path, "/bundle") {
		_, _ = w.Write(b.Data)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"item_id": b.ID, "catalog_version": version, "sha256": b.SHA256, "bytes": len(b.Data),
		"url": dl, "expires_at": "2026-11-24T12:10:00Z",
	})
}

type bundleAPI struct {
	srv       *httptest.Server
	bundle    atomic.Pointer[catalog.Bundle]
	version   atomic.Value
	status    atomic.Int32 // non-zero: the bundle route answers this
	downloads atomic.Int32
	auth      atomic.Value
	data      atomic.Pointer[[]byte] // overrides the archive bytes served
	mu        sync.Mutex
	events    []string
}

func (a *bundleAPI) eventBodies() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.events...)
}

func newBundleAPI(t *testing.T) *bundleAPI {
	t.Helper()
	a := &bundleAPI{}
	a.bundle.Store(testBundle(t, testCatalog, "white/a"))
	a.version.Store("v1")
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/tickets/white/a/bundle":
			a.auth.Store(r.Header.Get("Authorization"))
			if s := int(a.status.Load()); s != 0 {
				w.Header().Set("Retry-After", "7")
				w.WriteHeader(s)
				_, _ = w.Write([]byte(`{"error":"x"}`))
				return
			}
			serveBundle(w, r, a.bundle.Load(), a.version.Load().(string), "http://"+r.Host+"/dl/white/a")
		case "/dl/white/a":
			a.downloads.Add(1)
			if r.Header.Get("Authorization") != "" {
				t.Error("the presigned download must not carry the bearer token")
			}
			if d := a.data.Load(); d != nil {
				_, _ = w.Write(*d)
				return
			}
			_, _ = w.Write(a.bundle.Load().Data)
		case "/v1/events":
			raw, _ := io.ReadAll(r.Body)
			a.mu.Lock()
			a.events = append(a.events, string(raw))
			a.mu.Unlock()
			_, _ = w.Write([]byte(`{}`))
		default:
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(a.srv.Close)
	return a
}

func bundleEnv(t *testing.T, loggedIn bool) (*env, *bundleAPI) {
	t.Helper()
	api := newBundleAPI(t)
	e := newEnv(t)
	e.app.Catalog = fstest.MapFS{} // the embedded catalog must not be what starts
	e.app.Login = LoginConfig{APIURL: api.srv.URL}
	e.app.Getenv = func(string) string { return "" }
	if loggedIn {
		if err := state.SaveToken("tok"); err != nil {
			t.Fatal(err)
		}
	}
	return e, api
}

func bundlesIn(t *testing.T) (tars, sidecars []string) {
	t.Helper()
	dir, _ := state.Dir()
	tars, _ = filepath.Glob(filepath.Join(dir, "bundles", "*.tar.gz"))
	sidecars, _ = filepath.Glob(filepath.Join(dir, "bundles", "*.json"))
	return tars, sidecars
}

func readFile(t *testing.T, p string) []byte {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestStartFetchesCachesAndRecordsTicketStarted(t *testing.T) {
	e, api := bundleEnv(t, true)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if got := api.auth.Load(); got != "Bearer tok" {
		t.Errorf("auth = %v", got)
	}
	b := api.bundle.Load()
	tars, sidecars := bundlesIn(t)
	dir, _ := state.Dir()
	if len(tars) != 1 || tars[0] != filepath.Join(dir, "bundles", b.SHA256+".tar.gz") || len(sidecars) != 1 {
		t.Fatalf("cache = %v %v", tars, sidecars)
	}
	if !bytes.Equal(readFile(t, tars[0]), b.Data) {
		t.Error("cached bytes differ from the bundle")
	}
	var sc sidecar
	if err := json.Unmarshal(readFile(t, sidecars[0]), &sc); err != nil || sc != (sidecar{ItemID: "white/a", SHA256: b.SHA256, Bytes: int64(len(b.Data)), CatalogVersion: "v1"}) {
		t.Errorf("sidecar = %+v (%v)", sc, err)
	}
	if !strings.Contains(e.out.String(), "Fix A.") {
		t.Errorf("brief not shown:\n%s", e.out)
	}
	if err := e.run(t, "status"); err != nil || !strings.Contains(e.out.String(), "Fix A.") {
		t.Errorf("status reads the unpacked ticket: %v\n%s", err, e.out)
	}
}

func TestTicketStartedCarriesTheSidecarVersion(t *testing.T) {
	e, api := bundleEnv(t, true)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if v := catalogVersion("white/a"); v != "v1" {
		t.Fatalf("catalogVersion = %q", v)
	}
	var got []state.ProgressEvent
	for _, b := range api.eventBodies() {
		got = append(got, decodeEvents(t, b)...)
	}
	if len(got) == 0 || got[0].Type != "ticket.started" || got[0].CatalogVersion != "v1" || got[0].Lab != "local" {
		t.Errorf("events = %+v", got)
	}
}

func TestSecondStartSkipsTheDownloadAndRewritesTheSidecar(t *testing.T) {
	e, api := bundleEnv(t, true)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	api.version.Store("v2")
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if n := api.downloads.Load(); n != 1 {
		t.Errorf("downloads = %d, want 1", n)
	}
	if v := catalogVersion("white/a"); v != "v2" {
		t.Errorf("catalogVersion = %q, want v2", v)
	}
}

func TestChangedBundleReplacesTheOldOne(t *testing.T) {
	e, api := bundleEnv(t, true)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	old := api.bundle.Load()
	fixed := fstest.MapFS{}
	for k, v := range testCatalog {
		fixed[k] = v
	}
	fixed["white/a/ticket.yaml"] = &fstest.MapFile{Data: []byte(strings.Replace(string(testCatalog["white/a/ticket.yaml"].Data), "Fix A.", "Fix A, corrected.", 1))}
	api.bundle.Store(testBundle(t, fixed, "white/a"))
	api.version.Store("v2")
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	tars, sidecars := bundlesIn(t)
	if len(tars) != 1 || len(sidecars) != 1 || strings.Contains(tars[0], old.SHA256) || strings.Contains(sidecars[0], old.SHA256) {
		t.Errorf("one cached bundle expected, got %v %v", tars, sidecars)
	}
	if !strings.Contains(e.out.String(), "corrected") || api.downloads.Load() != 2 {
		t.Errorf("downloads %d\n%s", api.downloads.Load(), e.out)
	}
}

func TestOfflineStartUsesTheCachedBundle(t *testing.T) {
	e, api := bundleEnv(t, true)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	api.srv.Close()
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatalf("offline: %v", err)
	}
	if !strings.Contains(e.out.String(), "saved on this computer") || !strings.Contains(e.out.String(), "Fix A.") {
		t.Errorf("output:\n%s", e.out)
	}
}

func TestOfflineWithoutACacheSaysSo(t *testing.T) {
	e, api := bundleEnv(t, true)
	api.srv.Close()
	err := e.run(t, "start", "white/a")
	if err == nil || !strings.Contains(err.Error(), "not saved on this computer") {
		t.Errorf("err = %v", err)
	}
}

func TestBundleWithoutASidecarCountsAsNotCached(t *testing.T) {
	e, api := bundleEnv(t, true)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	_, sidecars := bundlesIn(t)
	if err := os.Remove(sidecars[0]); err != nil {
		t.Fatal(err)
	}
	api.srv.Close()
	if err := e.run(t, "start", "white/a"); err == nil {
		t.Error("a bundle without its sidecar must not start offline")
	}
}

func TestLoggedOutStartsFromCacheOrAsksForLogin(t *testing.T) {
	e, _ := bundleEnv(t, false)
	err := e.run(t, "start", "white/a")
	if err == nil || !strings.Contains(err.Error(), "dojo login") {
		t.Fatalf("err = %v", err)
	}
	e2, _ := bundleEnv(t, true)
	if err := e2.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if err := state.RemoveToken(); err != nil {
		t.Fatal(err)
	}
	if err := e2.run(t, "start", "white/a"); err != nil || !strings.Contains(e2.out.String(), "not logged in") {
		t.Errorf("logged out with a cache: %v\n%s", err, e2.out)
	}
}

func TestBundleRouteErrorsAreClear(t *testing.T) {
	for _, tc := range []struct {
		status int
		want   string
	}{
		{401, "dojo login"}, {403, "suspended"}, {404, "no ticket white/a"}, {429, "7s"},
	} {
		t.Run(strconv.Itoa(tc.status), func(t *testing.T) {
			e, api := bundleEnv(t, true)
			api.status.Store(int32(tc.status)) //nolint:gosec // G115: a small constant
			err := e.run(t, "start", "white/a")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q", err, tc.want)
			}
			if tars, _ := bundlesIn(t); len(tars) != 0 {
				t.Error("nothing is cached on an error")
			}
		})
	}
}

func TestServerErrorFallsBackToTheCache(t *testing.T) {
	e, api := bundleEnv(t, true)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	api.status.Store(500)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Errorf("a 500 with a cache should still start: %v", err)
	}
}

func TestFailedDownloadOfANewVersionFallsBackToTheCache(t *testing.T) {
	e, api := bundleEnv(t, true)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	old := api.bundle.Load()
	fixed := fstest.MapFS{}
	for k, v := range testCatalog {
		fixed[k] = v
	}
	fixed["white/a/check.sh"] = &fstest.MapFile{Data: []byte("CHECK2")}
	api.bundle.Store(testBundle(t, fixed, "white/a"))
	bad := []byte("garbage")
	api.data.Store(&bad)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatalf("a failed download with a cache should still start: %v", err)
	}
	if !strings.Contains(e.out.String(), "saved on this computer") {
		t.Errorf("output:\n%s", e.out)
	}
	if tars, _ := bundlesIn(t); len(tars) != 1 || !strings.Contains(tars[0], old.SHA256) {
		t.Errorf("the old bundle must stay: %v", tars)
	}
}

func TestFailedUnpackLeavesTheOldTreeInPlace(t *testing.T) {
	e, api := bundleEnv(t, true)
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	data := evilArchive(t, "../x", tar.TypeReg, "")
	sum := sha256.Sum256(data)
	api.bundle.Store(&catalog.Bundle{ID: "white/a", Data: data, SHA256: hex.EncodeToString(sum[:])})
	if err := e.run(t, "start", "white/a"); err == nil {
		t.Fatal("hostile bundle was accepted")
	}
	dir, _ := state.Dir()
	parent := filepath.Join(dir, "tickets", "white")
	names, _ := os.ReadDir(parent)
	if len(names) != 1 || names[0].Name() != "a" {
		t.Errorf("tickets/white holds %v, want only a", names)
	}
	if _, err := os.Stat(filepath.Join(parent, "a", "ticket.yaml")); err != nil {
		t.Errorf("the old tree is gone: %v", err)
	}
}

func TestEmbeddedFallbackOnlyWhenTheTicketDirIsAbsent(t *testing.T) {
	e, _ := bundleEnv(t, true)
	e.app.Catalog = testCatalog
	st, _ := state.Load()
	st.Current = "white/a"
	if err := st.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.loadTicket("white/a"); err != nil {
		t.Fatalf("no tree: falls back to the catalog: %v", err)
	}
	dir, _ := state.Dir()
	if err := os.MkdirAll(filepath.Join(dir, "tickets", "white", "a"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.app.loadTicket("white/a"); err == nil {
		t.Error("a present but broken tree must not fall back to the catalog")
	}
}

func TestDownloadChecksSizeAndHash(t *testing.T) {
	for name, data := range map[string][]byte{
		"other bytes": append(bytes.Clone(testBundle(t, testCatalog, "white/a").Data[:10]), make([]byte, 20)...),
		"truncated":   testBundle(t, testCatalog, "white/a").Data[:20],
	} {
		t.Run(name, func(t *testing.T) {
			e, api := bundleEnv(t, true)
			api.data.Store(&data)
			err := e.run(t, "start", "white/a")
			if err == nil || !strings.Contains(err.Error(), "does not match") {
				t.Errorf("err = %v", err)
			}
			if tars, sc := bundlesIn(t); len(tars)+len(sc) != 0 {
				t.Error("a refused download must not be cached")
			}
		})
	}
}

func evilArchive(t *testing.T, name string, typeflag byte, link string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("x")
	hdr := &tar.Header{Name: name, Typeflag: typeflag, Mode: 0o644, Size: int64(len(body)), Linkname: link}
	if typeflag != tar.TypeReg {
		hdr.Size = 0
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if typeflag == tar.TypeReg {
		_, _ = tw.Write(body)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestHostileArchivesAreRefusedBeforeAnythingIsWritten(t *testing.T) {
	for name, data := range map[string][]byte{
		"parent dir": evilArchive(t, "../x", tar.TypeReg, ""),
		"absolute":   evilArchive(t, "/etc/x", tar.TypeReg, ""),
		"backslash":  evilArchive(t, `files\x`, tar.TypeReg, ""),
		"symlink":    evilArchive(t, "files/l", tar.TypeSymlink, "/etc"),
		"hard link":  evilArchive(t, "files/l", tar.TypeLink, "ticket.yaml"),
	} {
		t.Run(name, func(t *testing.T) {
			e, api := bundleEnv(t, true)
			sum := sha256.Sum256(data)
			b := &catalog.Bundle{ID: "white/a", Data: data, SHA256: hex.EncodeToString(sum[:])}
			api.bundle.Store(b)
			if err := e.run(t, "start", "white/a"); err == nil {
				t.Fatal("hostile bundle was accepted")
			}
			dir, _ := state.Dir()
			if _, err := os.Stat(filepath.Join(dir, "tickets", "white", "a")); err == nil {
				t.Error("the ticket directory was left behind")
			}
			if tars, sc := bundlesIn(t); len(tars)+len(sc) != 0 {
				t.Error("a refused bundle must not be cached")
			}
		})
	}
}

func TestDevCatalogSkipsTheAPI(t *testing.T) {
	e, api := bundleEnv(t, true)
	e.app.Catalog = testCatalog
	e.app.Getenv = func(k string) string {
		if k == "DOJO_KATAS" {
			return "/some/dir"
		}
		return ""
	}
	if err := e.run(t, "start", "white/a"); err != nil {
		t.Fatal(err)
	}
	if api.auth.Load() != nil || api.downloads.Load() != 0 {
		t.Error("DOJO_KATAS must not call the bundle route")
	}
}
