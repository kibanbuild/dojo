package catalog

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"
	"time"
)

// Bundle caps from section 3.8. The build fails over one; the CLI checks them again on read.
const (
	MaxBundleBytes   = 1 << 20 // compressed
	MaxUnpackedBytes = 4 << 20 // sum of file sizes
	MaxBundleEntries = 64      // files and directories

	// maxName keeps every name in the tar header's own 100-byte field, so the archive needs no
	// PAX or USTAR-prefix records.
	maxName = 100

	// Whatever the tar framing adds on top of the file bytes: a header block and padding per entry,
	// and the end-of-archive blocks.
	maxFraming = MaxBundleEntries*1024 + 2048
)

// Bundle is a built ticket archive: the exact bytes to upload and their hash.
type Bundle struct {
	ID       string // <role>/<ticket>
	Data     []byte // the tar.gz
	SHA256   string // lower-case hex of Data
	Unpacked int64  // sum of file sizes
	Entries  int    // files and directories
}

// Key is the immutable object key: bundles/<role>/<ticket>/<sha256>.tar.gz.
func (b *Bundle) Key() string { return BundleKey(b.ID, b.SHA256) }

// BundleKey builds the object key of a ticket id and a bundle hash.
func BundleKey(id, sha string) string { return "bundles/" + id + "/" + sha + ".tar.gz" }

type entry struct {
	name string // no trailing slash
	dir  bool
	exec bool
	data []byte
}

// validName refuses a name that is empty, absolute, holds "." or ".." segments, a backslash, an
// empty segment, anything but printable ASCII, or is longer than the tar name field.
func validName(name string) error {
	if name == "" || len(name) > maxName {
		return fmt.Errorf("name must be 1 to %d bytes", maxName)
	}
	if strings.HasPrefix(name, "/") {
		return errors.New("absolute path")
	}
	for _, seg := range strings.Split(name, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return fmt.Errorf("segment %q is not allowed", seg)
		}
	}
	for i := 0; i < len(name); i++ {
		if c := name[i]; c < 0x20 || c > 0x7e || c == '\\' {
			return errors.New("only printable ASCII without backslash is allowed")
		}
	}
	return nil
}

func isScript(name string) bool { return strings.HasSuffix(name, ".sh") }

// inBundle reports whether a name may appear at the archive root: the three files, or files/.
func inBundle(name string, dir bool) bool {
	switch name {
	case ticketFileName, "setup.sh", "check.sh":
		return !dir
	case "files":
		return dir
	}
	return strings.HasPrefix(name, "files/")
}

// BuildBundle validates the ticket <role>/<ticket> in the tree and builds its bundle:
// ticket.yaml, setup.sh, check.sh and the optional files/ tree. Anything else in the directory
// (solutions, the wrong-fix tests) is left out. Links and devices fail the build, and so does a
// cap. The bytes are reproducible: the same content gives the same hash whatever the file times
// or the order the files were created in. ticket.yaml, setup.sh and check.sh enter with LF line
// endings; everything under files/ is byte-exact, so authors commit it with LF.
func BuildBundle(tree fs.FS, id string) (*Bundle, error) {
	t, err := LoadTicket(tree, id)
	if err != nil {
		return nil, err
	}
	if err := noSymlinkAbove(tree, id); err != nil {
		return nil, err
	}
	ticketRaw, err := fs.ReadFile(tree, path.Join(id, ticketFileName))
	if err != nil {
		return nil, err
	}
	// ticket.yaml is text and part of the hash, so a CRLF checkout must not change it.
	ticketRaw = bytes.ReplaceAll(ticketRaw, []byte("\r\n"), []byte("\n"))
	entries := []entry{
		{name: ticketFileName, data: ticketRaw},
		{name: "setup.sh", data: t.Setup, exec: true},
		{name: "check.sh", data: t.Check, exec: true},
	}
	for _, f := range entries {
		info, err := fs.Lstat(tree, path.Join(id, f.name))
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("%s/%s: not a regular file (links and devices are refused)", id, f.name)
		}
	}
	extra, err := collectFiles(tree, id)
	if err != nil {
		return nil, err
	}
	entries = append(entries, extra...)
	return pack(id, entries)
}

// noSymlinkAbove refuses a ticket directory (or a parent of it) that is a link, which would let a
// ticket read files outside the catalog tree.
func noSymlinkAbove(tree fs.FS, id string) error {
	p := ""
	for _, seg := range strings.Split(id, "/") {
		p = path.Join(p, seg)
		info, err := fs.Lstat(tree, p)
		if err != nil {
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("%s: not a directory (links are refused)", p)
		}
	}
	return nil
}

// collectFiles walks <id>/files without following links.
func collectFiles(tree fs.FS, id string) ([]entry, error) {
	root := path.Join(id, "files")
	info, err := fs.Lstat(tree, root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s: files must be a directory (links are refused)", root)
	}
	var out []entry
	var total int64
	err = fs.WalkDir(tree, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := strings.TrimPrefix(p, id+"/")
		if err := validName(name); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if err := notASolution(name); err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		switch {
		case d.IsDir():
			out = append(out, entry{name: name, dir: true})
		case d.Type().IsRegular():
			fi, err := d.Info()
			if err != nil {
				return err
			}
			if total += fi.Size(); total > MaxUnpackedBytes {
				return fmt.Errorf("%s: the bundle would unpack to more than %d bytes", p, MaxUnpackedBytes)
			}
			b, err := fs.ReadFile(tree, p)
			if err != nil {
				return err
			}
			out = append(out, entry{name: name, data: b, exec: isScript(name)})
		default:
			return fmt.Errorf("%s: %s is refused (only regular files and directories)", p, d.Type())
		}
		return nil
	})
	return out, err
}

// notASolution refuses a path segment under files/ that looks like a solution or a wrong fix, so
// one can never ship by being left in the wrong directory.
func notASolution(name string) error {
	for _, seg := range strings.Split(name, "/") {
		low := strings.ToLower(seg)
		if strings.HasPrefix(low, "wrong") || strings.HasPrefix(low, "solution") {
			return fmt.Errorf("%q looks like a solution or a wrong fix, which must never ship (keep it outside files/)", seg)
		}
	}
	return nil
}

// foldCollision refuses two names that are equal once lower-cased: they would overwrite each
// other on a case-insensitive disk (macOS, Windows).
func foldCollision(names []string) error {
	seen := map[string]string{}
	for _, n := range names {
		k := strings.ToLower(n)
		if other, ok := seen[k]; ok {
			return fmt.Errorf("%q and %q differ only in letter case and would collide on a case-insensitive disk", other, n)
		}
		seen[k] = n
	}
	return nil
}

// pack writes the reproducible archive.
func pack(id string, entries []entry) (*Bundle, error) {
	if len(entries) > MaxBundleEntries {
		return nil, fmt.Errorf("%s: %d entries, the cap is %d", id, len(entries), MaxBundleEntries)
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.name
	}
	if err := foldCollision(names); err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	var unpacked int64
	for _, e := range entries {
		unpacked += int64(len(e.data))
	}
	if unpacked > MaxUnpackedBytes {
		return nil, fmt.Errorf("%s: unpacks to %d bytes, the cap is %d", id, unpacked, MaxUnpackedBytes)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })

	var buf bytes.Buffer
	// Level, header and tool are part of the contract: a change may change every hash.
	gz, err := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Typeflag: tar.TypeReg,
			Mode:     0o644,
			ModTime:  time.Unix(0, 0),
			Format:   tar.FormatUSTAR,
			Size:     int64(len(e.data)),
		}
		if e.exec {
			hdr.Mode = 0o755
		}
		if e.dir {
			hdr.Name += "/"
			hdr.Typeflag = tar.TypeDir
			hdr.Mode = 0o755
			hdr.Size = 0
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, fmt.Errorf("%s: %s: %w", id, e.name, err)
		}
		if _, err := tw.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if buf.Len() > MaxBundleBytes {
		return nil, fmt.Errorf("%s: compressed bundle is %d bytes, the cap is %d", id, buf.Len(), MaxBundleBytes)
	}
	sum := sha256.Sum256(buf.Bytes())
	return &Bundle{ID: id, Data: buf.Bytes(), SHA256: hex.EncodeToString(sum[:]), Unpacked: unpacked, Entries: len(entries)}, nil
}

var errTooBig = errors.New("bundle unpacks to more than the cap (refused)")

// capReader fails, rather than ends quietly, once more than n bytes were read.
type capReader struct {
	r io.Reader
	n int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.n <= 0 {
		// Only an end of stream right now is fine.
		var one [1]byte
		if n, err := c.r.Read(one[:]); n > 0 {
			return 0, errTooBig
		} else if err != nil {
			return 0, err
		}
		return 0, nil
	}
	if int64(len(p)) > c.n {
		p = p[:c.n]
	}
	n, err := c.r.Read(p)
	c.n -= int64(n)
	return n, err
}

// ExtractBundle checks a downloaded bundle and unpacks it into dst, which must not exist or be
// empty. It verifies the size cap and the sha256 (lower-case hex) before it decompresses anything,
// then reads the whole archive in memory under the entry and unpacked caps, refusing an absolute
// path, "..", a backslash, a link or any entry that is not a file or directory, and only then
// writes. It returns the ticket parsed from the bundle, with its Setup and Check.
func ExtractBundle(data []byte, wantSHA, dst string) (*Ticket, error) {
	entries, err := readBundle(data, wantSHA)
	if err != nil {
		return nil, err
	}
	var t *Ticket
	t, err = ticketFromEntries(entries)
	if err != nil {
		return nil, err
	}
	if err := writeEntries(entries, dst); err != nil {
		return nil, err
	}
	return t, nil
}

func readBundle(data []byte, wantSHA string) ([]entry, error) {
	if len(data) > MaxBundleBytes {
		return nil, fmt.Errorf("bundle is %d bytes, the cap is %d", len(data), MaxBundleBytes)
	}
	sum := sha256.Sum256(data)
	if got := hex.EncodeToString(sum[:]); got != wantSHA {
		return nil, fmt.Errorf("bundle sha256 is %s, want %s", got, wantSHA)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("bundle: %w", err)
	}
	defer func() { _ = gz.Close() }()
	capped := &capReader{r: gz, n: MaxUnpackedBytes + maxFraming}
	tr := tar.NewReader(capped)

	var entries []entry
	seen := map[string]bool{}
	folded := map[string]string{}
	var total int64
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("bundle: %w", err)
		}
		if len(entries) >= MaxBundleEntries {
			return nil, fmt.Errorf("bundle has more than %d entries", MaxBundleEntries)
		}
		var isDir bool
		switch hdr.Typeflag {
		case tar.TypeReg:
		case tar.TypeDir:
			isDir = true
		default:
			return nil, fmt.Errorf("bundle entry %q: type %q is refused (only files and directories)", hdr.Name, hdr.Typeflag)
		}
		name := hdr.Name
		if isDir {
			name = strings.TrimSuffix(name, "/")
		}
		if err := validName(name); err != nil {
			return nil, fmt.Errorf("bundle entry %q: %w", hdr.Name, err)
		}
		if !inBundle(name, isDir) {
			return nil, fmt.Errorf("bundle entry %q is not part of the bundle format", hdr.Name)
		}
		if seen[name] {
			return nil, fmt.Errorf("bundle entry %q appears twice", hdr.Name)
		}
		seen[name] = true
		// A case-insensitive disk would write one over the other and leave a half-written tree.
		// Every parent counts too: "files/Dir/x" and "files/dir/y" meet in one directory there.
		for p := name; p != "."; p = path.Dir(p) {
			low := strings.ToLower(p)
			if other, ok := folded[low]; ok && other != p {
				return nil, fmt.Errorf("bundle paths %q and %q differ only in letter case", other, p)
			}
			folded[low] = p
		}
		if hdr.Size < 0 || (isDir && hdr.Size != 0) {
			return nil, fmt.Errorf("bundle entry %q has a bad size", hdr.Name)
		}
		if total += hdr.Size; total > MaxUnpackedBytes {
			return nil, fmt.Errorf("bundle unpacks to more than %d bytes (refused)", MaxUnpackedBytes)
		}
		body, err := io.ReadAll(io.LimitReader(tr, hdr.Size+1))
		if err != nil {
			return nil, fmt.Errorf("bundle entry %q: %w", hdr.Name, err)
		}
		if int64(len(body)) != hdr.Size {
			return nil, fmt.Errorf("bundle entry %q is shorter than its header says", hdr.Name)
		}
		entries = append(entries, entry{name: name, dir: isDir, exec: hdr.Mode&0o111 != 0, data: body})
	}
	// Trailing padding is fine; anything past the cap is a bomb.
	if _, err := io.Copy(io.Discard, capped); err != nil {
		return nil, fmt.Errorf("bundle: %w", err)
	}
	return entries, nil
}

// ticketFromEntries checks the three required files and the paths under files/, and parses
// ticket.yaml.
func ticketFromEntries(entries []entry) (*Ticket, error) {
	byName := map[string]entry{}
	for _, e := range entries {
		byName[e.name] = e
	}
	for _, e := range entries {
		// A file cannot also be a directory of another entry.
		for p := path.Dir(e.name); p != "."; p = path.Dir(p) {
			if parent, ok := byName[p]; ok && !parent.dir {
				return nil, fmt.Errorf("bundle entry %q sits under the file %q", e.name, p)
			}
		}
	}
	for _, n := range []string{ticketFileName, "setup.sh", "check.sh"} {
		if _, ok := byName[n]; !ok {
			return nil, fmt.Errorf("bundle has no %s", n)
		}
	}
	t, err := ParseTicket("bundle/"+ticketFileName, byName[ticketFileName].data)
	if err != nil {
		return nil, err
	}
	t.Setup = byName["setup.sh"].data
	t.Check = byName["check.sh"].data
	return t, nil
}

// writeEntries writes into an absent or empty dst. os.Root keeps every write inside it.
// A failed write removes what it wrote, so dst is left as it was found and a retry works.
func writeEntries(entries []entry, dst string) (err error) {
	_, statErr := os.Lstat(dst)
	created := errors.Is(statErr, fs.ErrNotExist)
	if err := os.MkdirAll(dst, 0o750); err != nil {
		return err
	}
	existing, err := os.ReadDir(dst)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return fmt.Errorf("%s is not empty", dst)
	}
	root, err := os.OpenRoot(dst)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	defer func() {
		if err == nil {
			return
		}
		if created {
			_ = os.RemoveAll(dst)
			return
		}
		for _, name := range mustNames(dst) {
			_ = root.RemoveAll(name)
		}
	}()
	return writeAll(root, entries)
}

// mustNames lists the entries of dir, or none when it cannot be read.
func mustNames(dir string) []string {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, len(ents))
	for i, e := range ents {
		names[i] = e.Name()
	}
	return names
}

func writeAll(root *os.Root, entries []entry) error {
	for _, e := range entries {
		if e.dir {
			if err := root.MkdirAll(e.name, 0o755); err != nil {
				return err
			}
			continue
		}
		if dir := path.Dir(e.name); dir != "." {
			if err := root.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
		mode := os.FileMode(0o644)
		if e.exec {
			mode = 0o755
		}
		f, err := root.OpenFile(e.name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		if err != nil {
			return err
		}
		_, werr := f.Write(e.data)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
	}
	return nil
}
