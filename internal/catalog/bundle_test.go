package catalog_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/kibanyadev/dojo/internal/catalog"
)

const fixtureID = "support-l1/perms-01"

func fixtureFS() fs.FS { return os.DirFS("testdata/catalog") }

func listTar(t *testing.T, data []byte) []*tar.Header {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if gz.Name != "" || !gz.ModTime.IsZero() && gz.ModTime.Unix() != 0 {
		t.Errorf("gzip header carries name %q or time %v", gz.Name, gz.ModTime)
	}
	tr := tar.NewReader(gz)
	var out []*tar.Header
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, h)
	}
}

func TestBuildBundleContentAndFormat(t *testing.T) {
	b, err := catalog.BuildBundle(fixtureFS(), fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b.Data)
	if b.SHA256 != hex.EncodeToString(sum[:]) {
		t.Error("SHA256 is not the hash of Data")
	}
	if want := "bundles/support-l1/perms-01/" + b.SHA256 + ".tar.gz"; b.Key() != want {
		t.Errorf("key = %s, want %s", b.Key(), want)
	}
	var names []string
	for _, h := range listTar(t, b.Data) {
		names = append(names, h.Name)
		if h.ModTime.Unix() != 0 || h.Uid != 0 || h.Gid != 0 || h.Uname != "" || h.Gname != "" {
			t.Errorf("%s: mtime %v uid %d gid %d user %q group %q", h.Name, h.ModTime, h.Uid, h.Gid, h.Uname, h.Gname)
		}
		wantMode := int64(0o644)
		if h.Typeflag == tar.TypeDir || strings.HasSuffix(h.Name, ".sh") {
			wantMode = 0o755
		}
		if h.Mode != wantMode {
			t.Errorf("%s: mode %o, want %o", h.Name, h.Mode, wantMode)
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
			t.Errorf("%s: type %q", h.Name, h.Typeflag)
		}
	}
	want := "check.sh files/ files/notes/ files/notes/readme.txt files/seed.sh setup.sh ticket.yaml"
	if got := strings.Join(names, " "); got != want {
		t.Errorf("entries = %s\nwant      %s", got, want)
	}
	if b.Entries != 7 {
		t.Errorf("Entries = %d", b.Entries)
	}
	// The wrong-fix directory and anything else stay out.
	for _, n := range names {
		if strings.Contains(n, "wrong") || strings.Contains(n, "solution") {
			t.Errorf("%s must not ship", n)
		}
	}
}

func TestBuildBundleIsDeterministic(t *testing.T) {
	first, err := catalog.BuildBundle(fixtureFS(), fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	second, err := catalog.BuildBundle(fixtureFS(), fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	if first.SHA256 != second.SHA256 || !bytes.Equal(first.Data, second.Data) {
		t.Error("two builds differ")
	}

	// A copy on disk with other mtimes, created in the opposite order, gives the same bytes.
	dir := t.TempDir()
	var paths []string
	err = fs.WalkDir(fixtureFS(), fixtureID, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			paths = append(paths, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := len(paths) - 1; i >= 0; i-- {
		raw, err := fs.ReadFile(fixtureFS(), paths[i])
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, paths[i])
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, raw, 0o600); err != nil { // other file modes too
			t.Fatal(err)
		}
		when := time.Unix(1_000_000+int64(i)*86400, 0)
		if err := os.Chtimes(dst, when, when); err != nil {
			t.Fatal(err)
		}
	}
	// The ticket needs catalog.yaml only for Load; BuildBundle works from the ticket directory.
	third, err := catalog.BuildBundle(os.DirFS(dir), fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	if third.SHA256 != first.SHA256 {
		t.Errorf("touching mtimes, modes and creation order changed the hash: %s vs %s", third.SHA256, first.SHA256)
	}

	// A content change does change it.
	if err := os.WriteFile(filepath.Join(dir, fixtureID, "check.sh"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fourth, err := catalog.BuildBundle(os.DirFS(dir), fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	if fourth.SHA256 == first.SHA256 {
		t.Error("different content, same hash")
	}
}

func TestBuildBundleNormalisesCRLFScripts(t *testing.T) {
	m := fixtureTree()
	m["support-l1/perms-01/setup.sh"] = &fstest.MapFile{Data: []byte("a\r\nb\r\n")}
	crlf, err := catalog.BuildBundle(m, fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	m["support-l1/perms-01/setup.sh"] = &fstest.MapFile{Data: []byte("a\nb\n")}
	lf, err := catalog.BuildBundle(m, fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	if crlf.SHA256 != lf.SHA256 {
		t.Error("a CRLF checkout changes the bundle")
	}
}

func TestBuildBundleNormalisesCRLFTicketYAML(t *testing.T) {
	m := fixtureTree()
	lf, err := catalog.BuildBundle(m, fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	m["support-l1/perms-01/ticket.yaml"] = &fstest.MapFile{Data: []byte(strings.ReplaceAll(validTicket, "\n", "\r\n"))}
	crlf, err := catalog.BuildBundle(m, fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	if crlf.SHA256 != lf.SHA256 {
		t.Error("a CRLF ticket.yaml changes the bundle")
	}
}

func TestBuildBundleKeepsFilesByteExact(t *testing.T) {
	build := func(body string) string {
		m := fixtureTree()
		m["support-l1/perms-01/files/a.txt"] = &fstest.MapFile{Data: []byte(body)}
		b, err := catalog.BuildBundle(m, fixtureID)
		if err != nil {
			t.Fatal(err)
		}
		return b.SHA256
	}
	if build("x\n") == build("x\r\n") {
		t.Error("files/ content must be byte-exact")
	}
}

func TestBuildBundleRefusesCaseCollisions(t *testing.T) {
	tests := map[string][]string{
		"two files":            {"files/Foo", "files/foo"},
		"file and directory":   {"files/d", "files/D/x"},
		"two directories":      {"files/Sub/a", "files/sub/b"},
		"upper-case extension": {"files/a.TXT", "files/a.txt"},
	}
	for name, paths := range tests {
		t.Run(name, func(t *testing.T) {
			m := fixtureTree()
			for _, p := range paths {
				m["support-l1/perms-01/"+p] = &fstest.MapFile{Data: []byte("x")}
			}
			if b, err := catalog.BuildBundle(m, fixtureID); err == nil || !strings.Contains(err.Error(), "case") {
				t.Errorf("built %v, err %v; want a case-collision refusal", b != nil, err)
			}
		})
	}
}

func TestBuildBundleRefusesSolutionsAndWrongFixes(t *testing.T) {
	for _, p := range []string{
		"files/solution.sh", "files/solution/x", "files/Solution.sh", "files/wrong-fix.sh",
		"files/wrong/a", "files/WRONG-1", "files/sub/solution.sh",
	} {
		m := fixtureTree()
		m["support-l1/perms-01/"+p] = &fstest.MapFile{Data: []byte("x")}
		if _, err := catalog.BuildBundle(m, fixtureID); err == nil || !strings.Contains(err.Error(), "solution or a wrong fix") {
			t.Errorf("%s: err = %v, want a refusal", p, err)
		}
	}
	m := fixtureTree()
	m["support-l1/perms-01/files/wronger-than-ever/x"] = &fstest.MapFile{Data: []byte("x")}
	m["support-l1/perms-01/solution.sh"] = &fstest.MapFile{Data: []byte("outside files/ is simply left out")}
	delete(m, "support-l1/perms-01/files/wronger-than-ever/x")
	if _, err := catalog.BuildBundle(m, fixtureID); err != nil {
		t.Errorf("solution.sh next to the ticket must be left out, not refused: %v", err)
	}
}

// A backslash cannot be a file name on Windows, so this case is built in memory.
func TestBuildBundleRefusesBackslashNames(t *testing.T) {
	m := fixtureTree()
	m["support-l1/perms-01/files/a\\b"] = &fstest.MapFile{Data: []byte("x")}
	if _, err := catalog.BuildBundle(m, fixtureID); err == nil {
		t.Error("a name with a backslash was bundled")
	}
}

func TestBuildBundleRefusesWindowsUnsafeNames(t *testing.T) {
	for _, name := range []string{"con", "PRN", "Aux.txt", "nul.tar.gz", "com1", "COM9.x", "lpt5", "a:b", "a.", "dir./x"} {
		m := fixtureTree()
		m["support-l1/perms-01/files/"+name] = &fstest.MapFile{Data: []byte("x")}
		if _, err := catalog.BuildBundle(m, fixtureID); err == nil {
			t.Errorf("%q was bundled", name)
		}
	}
	for _, name := range []string{"com0", "console", "comma", "lpt10", "a.b"} {
		m := fixtureTree()
		m["support-l1/perms-01/files/"+name] = &fstest.MapFile{Data: []byte("x")}
		if _, err := catalog.BuildBundle(m, fixtureID); err != nil {
			t.Errorf("%q refused: %v", name, err)
		}
	}
}

func TestBuildBundleRefusals(t *testing.T) {
	// A real directory is needed for links.
	copyTicket := func(t *testing.T) (string, string) {
		dir := t.TempDir()
		tk := filepath.Join(dir, fixtureID)
		if err := os.MkdirAll(tk, 0o755); err != nil {
			t.Fatal(err)
		}
		for n, c := range map[string]string{"ticket.yaml": validTicket, "setup.sh": "s", "check.sh": "c"} {
			if err := os.WriteFile(filepath.Join(tk, n), []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir, tk
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(t *testing.T, dir, tk string){
		"symlink in files": func(t *testing.T, _, tk string) {
			must(t, os.MkdirAll(filepath.Join(tk, "files"), 0o755))
			must(t, os.Symlink(outside, filepath.Join(tk, "files", "link")))
		},
		"symlink to a dir in files": func(t *testing.T, _, tk string) {
			must(t, os.MkdirAll(filepath.Join(tk, "files"), 0o755))
			must(t, os.Symlink(filepath.Dir(outside), filepath.Join(tk, "files", "d")))
		},
		"symlinked setup.sh": func(t *testing.T, _, tk string) {
			must(t, os.Remove(filepath.Join(tk, "setup.sh")))
			must(t, os.Symlink(outside, filepath.Join(tk, "setup.sh")))
		},
		"symlinked files dir": func(t *testing.T, _, tk string) {
			must(t, os.Symlink(filepath.Dir(outside), filepath.Join(tk, "files")))
		},
		"files is a regular file": func(t *testing.T, _, tk string) {
			must(t, os.WriteFile(filepath.Join(tk, "files"), []byte("x"), 0o644))
		},
		"symlinked ticket dir": func(t *testing.T, dir, tk string) {
			real := filepath.Join(dir, "real")
			must(t, os.Rename(tk, real))
			must(t, os.Symlink(real, tk))
		},
		"non-ASCII name": func(t *testing.T, _, tk string) {
			must(t, os.MkdirAll(filepath.Join(tk, "files"), 0o755))
			must(t, os.WriteFile(filepath.Join(tk, "files", "é.txt"), []byte("x"), 0o644))
		},
		"name over 100 bytes": func(t *testing.T, _, tk string) {
			must(t, os.MkdirAll(filepath.Join(tk, "files"), 0o755))
			must(t, os.WriteFile(filepath.Join(tk, "files", strings.Repeat("a", 100)), []byte("x"), 0o644))
		},
		"65 entries": func(t *testing.T, _, tk string) {
			must(t, os.MkdirAll(filepath.Join(tk, "files"), 0o755))
			for i := 0; i < 62; i++ { // 3 + files/ + 62 = 66
				must(t, os.WriteFile(filepath.Join(tk, "files", "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), []byte("x"), 0o644))
			}
		},
		"over 4 MiB unpacked": func(t *testing.T, _, tk string) {
			must(t, os.MkdirAll(filepath.Join(tk, "files"), 0o755))
			must(t, os.WriteFile(filepath.Join(tk, "files", "big"), make([]byte, catalog.MaxUnpackedBytes+1), 0o644))
		},
		"over 1 MiB compressed": func(t *testing.T, _, tk string) {
			must(t, os.MkdirAll(filepath.Join(tk, "files"), 0o755))
			noise := make([]byte, catalog.MaxBundleBytes+1)
			x := uint32(1)
			for i := range noise { // incompressible, deterministic
				x = x*1664525 + 1013904223
				noise[i] = byte(x >> 24)
			}
			must(t, os.WriteFile(filepath.Join(tk, "files", "noise"), noise, 0o644))
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			dir, tk := copyTicket(t)
			mutate(t, dir, tk)
			if b, err := catalog.BuildBundle(os.DirFS(dir), fixtureID); err == nil {
				t.Errorf("built a bundle of %d bytes, want a refusal", len(b.Data))
			}
		})
	}

	t.Run("exactly 64 entries is fine", func(t *testing.T) {
		dir, tk := copyTicket(t)
		must(t, os.MkdirAll(filepath.Join(tk, "files"), 0o755))
		for i := 0; i < 60; i++ { // 3 + files/ + 60 = 64
			must(t, os.WriteFile(filepath.Join(tk, "files", "f"+string(rune('a'+i%26))+string(rune('a'+i/26))), []byte("x"), 0o644))
		}
		if b, err := catalog.BuildBundle(os.DirFS(dir), fixtureID); err != nil || b.Entries != 64 {
			t.Errorf("entries %v, err %v", b, err)
		}
	})
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestBuildBundleRefusesInvalidTicket(t *testing.T) {
	m := fixtureTree()
	m["support-l1/perms-01/ticket.yaml"] = &fstest.MapFile{Data: []byte(replace(validTicket, "hints:", "hint:"))}
	if _, err := catalog.BuildBundle(m, fixtureID); err == nil {
		t.Error("an invalid ticket.yaml must not be bundled")
	}
	if _, err := catalog.BuildBundle(m, "support-l1/missing"); err == nil {
		t.Error("an unknown ticket must not be bundled")
	}
}

// rawBundle builds a gzip-ed tar from headers and bodies, bypassing BuildBundle's checks.
type rawEntry struct {
	hdr  tar.Header
	body string
}

func rawBundle(t *testing.T, entries ...rawEntry) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := e.hdr
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		must(t, tw.WriteHeader(&h))
		_, err := tw.Write([]byte(e.body))
		must(t, err)
	}
	must(t, tw.Close())
	must(t, gz.Close())
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

func goodEntries() []rawEntry {
	return []rawEntry{
		{hdr: tar.Header{Name: "check.sh", Mode: 0o755}, body: "c"},
		{hdr: tar.Header{Name: "setup.sh", Mode: 0o755}, body: "s"},
		{hdr: tar.Header{Name: "ticket.yaml", Mode: 0o644}, body: validTicket},
	}
}

func TestExtractRoundTrip(t *testing.T) {
	b, err := catalog.BuildBundle(fixtureFS(), fixtureID)
	if err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "lab")
	tk, err := catalog.ExtractBundle(b.Data, b.SHA256, dst)
	if err != nil {
		t.Fatal(err)
	}
	if tk.ID != fixtureID || tk.Title == "" || len(tk.Setup) == 0 || len(tk.Check) == 0 {
		t.Errorf("ticket = %+v", tk)
	}
	for _, f := range []string{"ticket.yaml", "setup.sh", "check.sh", "files/seed.sh", "files/notes/readme.txt"} {
		want, err := fs.ReadFile(fixtureFS(), path.Join(fixtureID, f)) // io/fs paths use forward slashes
		must(t, err)
		got, err := os.ReadFile(filepath.Join(dst, f))
		if err != nil || !bytes.Equal(got, want) {
			t.Errorf("%s: %v, %q", f, err, got)
		}
	}
	if runtime.GOOS != "windows" { // Windows has no execute bit
		if fi, err := os.Stat(filepath.Join(dst, "files", "seed.sh")); err != nil || fi.Mode().Perm()&0o100 == 0 {
			t.Errorf("seed.sh should be executable: %v %v", fi, err)
		}
		if fi, err := os.Stat(filepath.Join(dst, "files", "notes", "readme.txt")); err != nil || fi.Mode().Perm()&0o111 != 0 {
			t.Errorf("readme.txt should not be executable: %v %v", fi, err)
		}
	}
	// Not into a used directory.
	if _, err := catalog.ExtractBundle(b.Data, b.SHA256, dst); err == nil {
		t.Error("extracted over a non-empty directory")
	}
}

func TestExtractRefusals(t *testing.T) {
	with := func(extra ...rawEntry) []rawEntry { return append(goodEntries(), extra...) }
	file := func(name string) rawEntry { return rawEntry{hdr: tar.Header{Name: name, Mode: 0o644}, body: "x"} }

	tests := map[string][]rawEntry{
		"parent traversal":                 with(file("files/../../evil")),
		"leading dot dot":                  with(file("../evil")),
		"absolute path":                    with(file("/etc/evil")),
		"backslash":                        with(file(`files\evil`)),
		"dot segment":                      with(file("files/./a")),
		"windows device":                   with(file("files/con")),
		"windows device with extension":    with(file("files/NUL.txt")),
		"windows device, numbered":         with(file("files/sub/Lpt3.log")),
		"colon":                            with(file("files/a:b")),
		"trailing dot":                     with(file("files/a.")),
		"trailing space":                   with(file("files/a ")),
		"empty segment":                    with(file("files//a")),
		"outside the format":               with(file("notes.txt")),
		"files as a file":                  with(file("files")),
		"symlink":                          with(rawEntry{hdr: tar.Header{Name: "files/l", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}),
		"hard link":                        with(rawEntry{hdr: tar.Header{Name: "files/l", Typeflag: tar.TypeLink, Linkname: "setup.sh"}}),
		"fifo":                             with(rawEntry{hdr: tar.Header{Name: "files/p", Typeflag: tar.TypeFifo}}),
		"char dev":                         with(rawEntry{hdr: tar.Header{Name: "files/c", Typeflag: tar.TypeChar}}),
		"duplicate":                        with(file("setup.sh")),
		"case collision, two files":        with(file("files/a"), file("files/A")),
		"case collision, dir + file":       with(rawEntry{hdr: tar.Header{Name: "files/D/", Typeflag: tar.TypeDir, Mode: 0o755}}, file("files/d")),
		"case collision, implicit parents": with(file("files/Dir/x"), file("files/dir/y")),
		"case collision, implicit parent vs dir entry": with(file("files/Dir/x"),
			rawEntry{hdr: tar.Header{Name: "files/dir/", Typeflag: tar.TypeDir, Mode: 0o755}}),
		"file under a file":   with(file("files/a"), file("files/a/b")),
		"missing ticket.yaml": goodEntries()[:2],
		"missing setup.sh":    {goodEntries()[0], goodEntries()[2]},
		"missing check.sh":    goodEntries()[1:],
		"bad ticket.yaml":     {goodEntries()[0], goodEntries()[1], {hdr: tar.Header{Name: "ticket.yaml", Mode: 0o644}, body: "nope: 1\n"}},
	}
	// 65 entries.
	many := goodEntries()
	for i := 0; i < 62; i++ {
		many = append(many, file("files/f"+string(rune('a'+i%26))+string(rune('a'+i/26))))
	}
	tests["65 entries"] = many
	// Declared size over the cap, with a body that is small: the header alone is refused.
	big := rawEntry{hdr: tar.Header{Name: "files/big", Mode: 0o644}, body: strings.Repeat("a", catalog.MaxUnpackedBytes+1)}
	tests["over 4 MiB"] = with(big)

	for name, entries := range tests {
		t.Run(name, func(t *testing.T) {
			data, sha := rawBundle(t, entries...)
			dst := filepath.Join(t.TempDir(), "lab")
			if _, err := catalog.ExtractBundle(data, sha, dst); err == nil {
				t.Fatal("extracted, want a refusal")
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(dst), "evil")); err == nil {
				t.Error("a file escaped the target directory")
			}
			if ents, _ := os.ReadDir(dst); len(ents) > 0 {
				t.Errorf("a refused bundle left %d entries behind", len(ents))
			}
		})
	}
}

func TestExtractChecksHashAndCaps(t *testing.T) {
	data, sha := rawBundle(t, goodEntries()...)
	dst := func() string { return filepath.Join(t.TempDir(), "lab") }

	if _, err := catalog.ExtractBundle(data, strings.Repeat("0", 64), dst()); err == nil || !strings.Contains(err.Error(), "sha256") {
		t.Errorf("wrong hash: %v", err)
	}
	if _, err := catalog.ExtractBundle(data, strings.ToUpper(sha), dst()); err == nil {
		t.Error("an upper-case hash is not the key format")
	}
	if _, err := catalog.ExtractBundle(data, sha, dst()); err != nil {
		t.Errorf("good bundle: %v", err)
	}
	flipped := append([]byte(nil), data...)
	flipped[len(flipped)/2] ^= 0xff
	if _, err := catalog.ExtractBundle(flipped, sha, dst()); err == nil {
		t.Error("a corrupted bundle passed")
	}
	if _, err := catalog.ExtractBundle(nil, sha, dst()); err == nil {
		t.Error("an empty bundle passed")
	}

	huge := make([]byte, catalog.MaxBundleBytes+1)
	sum := sha256.Sum256(huge)
	if _, err := catalog.ExtractBundle(huge, hex.EncodeToString(sum[:]), dst()); err == nil || !strings.Contains(err.Error(), "cap") {
		t.Errorf("over 1 MiB compressed: %v", err)
	}
}

// A small archive whose entries claim little but whose stream inflates far past the cap.
func TestExtractRefusesDecompressionBomb(t *testing.T) {
	var buf bytes.Buffer
	gz, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	tw := tar.NewWriter(gz)
	for _, e := range goodEntries() {
		h := e.hdr
		h.Typeflag, h.Size = tar.TypeReg, int64(len(e.body))
		must(t, tw.WriteHeader(&h))
		_, err := tw.Write([]byte(e.body))
		must(t, err)
	}
	must(t, tw.Close())
	// Padding after the end-of-archive marker: zeros that tar ignores and a naive reader drains.
	_, err := gz.Write(make([]byte, 64<<20))
	must(t, err)
	must(t, gz.Close())
	if buf.Len() > catalog.MaxBundleBytes {
		t.Fatalf("test bomb is %d bytes, should be small", buf.Len())
	}
	sum := sha256.Sum256(buf.Bytes())
	_, err = catalog.ExtractBundle(buf.Bytes(), hex.EncodeToString(sum[:]), filepath.Join(t.TempDir(), "lab"))
	if err == nil {
		t.Fatal("a 64 MiB stream was accepted")
	}
}

// One file whose body inflates past the cap while the header claims a small size is a short read;
// one that claims 4 MiB and delivers it plus more is cut at the header size.
func TestExtractRefusesBigClaimedFile(t *testing.T) {
	data, sha := rawBundle(t, append(goodEntries(), rawEntry{hdr: tar.Header{Name: "files/z", Mode: 0o644}, body: strings.Repeat("z", 3<<20)}, rawEntry{hdr: tar.Header{Name: "files/y", Mode: 0o644}, body: strings.Repeat("y", 2<<20)})...)
	if _, err := catalog.ExtractBundle(data, sha, filepath.Join(t.TempDir(), "lab")); err == nil {
		t.Error("5 MiB of files in total was accepted")
	}
}
