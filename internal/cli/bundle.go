package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/kibanyadev/dojo/internal/catalog"
	"github.com/kibanyadev/dojo/internal/state"
)

var shaRE = regexp.MustCompile(`^[0-9a-f]{64}$`)

// bundleAnswer is the body of GET /v1/tickets/{id}/bundle (design 3.8).
type bundleAnswer struct {
	ItemID         string `json:"item_id"`
	CatalogVersion string `json:"catalog_version"`
	SHA256         string `json:"sha256"`
	Bytes          int64  `json:"bytes"`
	URL            string `json:"url"`
}

// sidecar is DOJO_HOME/bundles/<sha256>.json; ticket.started reads catalog_version from it.
type sidecar struct {
	ItemID         string `json:"item_id"`
	SHA256         string `json:"sha256"`
	Bytes          int64  `json:"bytes"`
	CatalogVersion string `json:"catalog_version"`
}

// bundleMode is true when tickets come from the API's bundles; DOJO_KATAS or no API keeps the catalog.
func (a *App) bundleMode() bool { return a.eventsEnabled() }

// loadTicket reads a ticket from its unpacked bundle, or from the catalog outside bundle mode. A
// ticket started before bundles existed is still found in the catalog.
func (a *App) loadTicket(id string) (*catalog.Ticket, error) {
	if !a.bundleMode() {
		return catalog.LoadTicket(a.Catalog, id)
	}
	dir, err := state.Dir()
	if err != nil {
		return nil, err
	}
	if !catalog.ValidTicketID(id) {
		return catalog.LoadTicket(a.Catalog, id) // reports the bad id
	}
	if _, err := os.Lstat(filepath.Join(dir, "tickets", filepath.FromSlash(id))); errors.Is(err, fs.ErrNotExist) {
		return catalog.LoadTicket(a.Catalog, id)
	}
	return catalog.LoadTicket(os.DirFS(filepath.Join(dir, "tickets")), id)
}

// fetchTicket gets the ticket's bundle (API, download or cache), unpacks it into DOJO_HOME/tickets
// and returns the ticket.
func (a *App) fetchTicket(ctx context.Context, id string) (*catalog.Ticket, error) {
	if !catalog.ValidTicketID(id) {
		return nil, fmt.Errorf("%q is not a ticket id (expected <role>/<ticket>)", id)
	}
	token, err := state.LoadToken()
	switch {
	case errors.Is(err, state.ErrNoToken):
		return a.startCached(id, "you are not logged in", errors.New("not logged in: run dojo login, the first start of a ticket needs it"))
	case err != nil:
		return nil, err
	}
	r, err := a.callAPI(ctx, http.MethodGet, a.telemetryAPI(), "/v1/tickets/"+id+"/bundle", token, nil)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return nil, cerr
		}
		return a.startCached(id, "offline", netErr("cannot reach the Dojo API and this ticket is not saved on this computer", err))
	}
	switch r.status {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, errors.New("your login is no longer valid: run dojo login")
	case http.StatusForbidden:
		return nil, errors.New("this account cannot start tickets (suspended or not allowed)")
	case http.StatusNotFound:
		return nil, fmt.Errorf("no ticket %s: check the id", id)
	case http.StatusTooManyRequests:
		return nil, fmt.Errorf("too many requests: try again in %s", retryDelay(r.retryAfter))
	default:
		return a.startCached(id, "the API is not answering", fmt.Errorf("the Dojo API answered %d and this ticket is not saved on this computer", r.status))
	}
	var ans bundleAnswer
	if err := json.Unmarshal(r.body, &ans); err != nil || ans.ItemID != id || !shaRE.MatchString(ans.SHA256) ||
		ans.Bytes <= 0 || ans.Bytes > catalog.MaxBundleBytes {
		return nil, errors.New("the Dojo API answered the bundle request with something unexpected")
	}
	data, ok := a.cachedBySHA(id, ans.SHA256)
	if !ok {
		if data, err = a.download(ctx, ans); err != nil {
			if cerr := ctx.Err(); cerr != nil {
				return nil, cerr
			}
			return a.startCached(id, "the download failed: "+err.Error(), err)
		}
	}
	t, err := a.unpack(data, ans.SHA256, id)
	if err != nil {
		return nil, err
	}
	if err := a.saveBundle(ans, data, !ok); err != nil {
		return nil, fmt.Errorf("saving the ticket: %w", err)
	}
	return t, nil
}

// startCached unpacks the saved bundle of an item, or returns fail when there is none.
func (a *App) startCached(id, why string, fail error) (*catalog.Ticket, error) {
	sc, data, ok := a.cachedByItem(id)
	if !ok {
		return nil, fail
	}
	t, err := a.unpack(data, sc.SHA256, id)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(a.Out, "Using the copy of %s saved on this computer (%s).\n", id, why)
	return t, nil
}

func (a *App) download(ctx context.Context, ans bundleAnswer) ([]byte, error) {
	u, err := url.Parse(ans.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("the Dojo API gave a bundle address that cannot be used")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ans.URL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := a.httpClient().Do(req) // presigned: no bearer goes to the storage host
	if err != nil {
		return nil, netErr("downloading the ticket", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading the ticket: the storage answered %d (run dojo start again)", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, catalog.MaxBundleBytes+1))
	if err != nil {
		return nil, netErr("downloading the ticket", err)
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != ans.Bytes || hex.EncodeToString(sum[:]) != ans.SHA256 {
		return nil, errors.New("the downloaded ticket does not match its size or sha256 (refused)")
	}
	return data, nil
}

// unpack replaces DOJO_HOME/tickets/<id> with the bundle's content after catalog's checks.
func (a *App) unpack(data []byte, sha, id string) (*catalog.Ticket, error) {
	dir, err := state.Dir()
	if err != nil {
		return nil, err
	}
	dst := filepath.Join(dir, "tickets", filepath.FromSlash(id))
	parent := filepath.Dir(dst)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp(parent, ".new-*")
	if err != nil {
		return nil, err
	}
	t, err := catalog.ExtractBundle(data, sha, tmp)
	if err == nil && t.ID != id {
		err = fmt.Errorf("the bundle holds ticket %s (refused)", t.ID)
	}
	if err != nil {
		_ = os.RemoveAll(tmp)
		return nil, fmt.Errorf("ticket %s: %w", id, err)
	}
	// Windows cannot rename over a directory: move the old one away, the new one in, then drop the old.
	old := ""
	if _, serr := os.Lstat(dst); serr == nil {
		old = tmp + ".old"
		if err := os.Rename(dst, old); err != nil {
			_ = os.RemoveAll(tmp)
			return nil, err
		}
	}
	if err := os.Rename(tmp, dst); err != nil {
		if old != "" {
			_ = os.Rename(old, dst)
		}
		_ = os.RemoveAll(tmp)
		return nil, err
	}
	if old != "" {
		_ = os.RemoveAll(old)
	}
	return t, nil
}

func bundlesDir() (string, error) {
	dir, err := state.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bundles"), nil
}

func readSidecar(path string) (sidecar, bool) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: inside the dojo home
	if err != nil {
		return sidecar{}, false
	}
	var sc sidecar
	ok := json.Unmarshal(raw, &sc) == nil && shaRE.MatchString(sc.SHA256) &&
		strings.TrimSuffix(filepath.Base(path), ".json") == sc.SHA256
	return sc, ok
}

// readCached returns the bundle of a sidecar when the file is whole; a bundle that no longer
// matches its hash counts as not cached.
func readCached(dir string, sc sidecar) ([]byte, bool) {
	data, err := os.ReadFile(filepath.Join(dir, sc.SHA256+".tar.gz")) //nolint:gosec // G304: inside the dojo home
	if err != nil || int64(len(data)) != sc.Bytes {
		return nil, false
	}
	sum := sha256.Sum256(data)
	return data, hex.EncodeToString(sum[:]) == sc.SHA256
}

func (a *App) cachedBySHA(id, sha string) ([]byte, bool) {
	dir, err := bundlesDir()
	if err != nil {
		return nil, false
	}
	sc, ok := readSidecar(filepath.Join(dir, sha+".json"))
	if !ok || sc.ItemID != id {
		return nil, false
	}
	return readCached(dir, sc)
}

func (a *App) cachedByItem(id string) (sidecar, []byte, bool) {
	dir, err := bundlesDir()
	if err != nil {
		return sidecar{}, nil, false
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		if sc, ok := readSidecar(f); ok && sc.ItemID == id {
			if data, ok := readCached(dir, sc); ok {
				return sc, data, true
			}
		}
	}
	return sidecar{}, nil, false
}

// saveBundle stores the bundle (when new), rewrites the sidecar from the answer and, after a new
// download, deletes the item's older bundles.
func (a *App) saveBundle(ans bundleAnswer, data []byte, fresh bool) error {
	dir, err := bundlesDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if fresh {
		if err := writeAtomic(dir, ans.SHA256+".tar.gz", data); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(sidecar{ItemID: ans.ItemID, SHA256: ans.SHA256, Bytes: ans.Bytes, CatalogVersion: ans.CatalogVersion})
	if err != nil {
		return err
	}
	if err := writeAtomic(dir, ans.SHA256+".json", raw); err != nil {
		return err
	}
	if !fresh {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	for _, f := range files {
		if sc, ok := readSidecar(f); ok && sc.ItemID == ans.ItemID && sc.SHA256 != ans.SHA256 {
			_ = os.Remove(filepath.Join(dir, sc.SHA256+".tar.gz"))
			_ = os.Remove(f)
		}
	}
	return nil
}

// writeAtomic writes a 0600 file through a synced temp file and a rename.
func writeAtomic(dir, name string, data []byte) (err error) {
	f, err := os.CreateTemp(dir, ".bundle-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() {
		if err != nil {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if _, err = f.Write(data); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, name))
}
