package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kibanbuild/dojo/internal/catalog"
	"github.com/kibanbuild/dojo/internal/server/repo"
)

const (
	bundleTicketsPrefix = "/v1/tickets/"
	bundleSuffix        = "/bundle"
	// bundleTTL is how long a presigned bundle URL works (design 3.8).
	bundleTTL = 10 * time.Minute
)

// BundleStore reads a ticket's bundle columns.
type BundleStore interface {
	GetTicketBundle(ctx context.Context, id string) (repo.GetTicketBundleRow, error)
}

// BundleSigner presigns a download of an object key.
type BundleSigner interface {
	PresignGet(ctx context.Context, key string, ttl time.Duration) (string, error)
}

// Bundles serves GET /v1/tickets/{id}/bundle (design 3.8).
type Bundles struct {
	Store  BundleStore
	Signer BundleSigner
}

type bundleResponse struct {
	ItemID         string `json:"item_id"`
	CatalogVersion string `json:"catalog_version"`
	SHA256         string `json:"sha256"`
	Bytes          int32  `json:"bytes"`
	URL            string `json:"url"`
	ExpiresAt      string `json:"expires_at"`
}

// bundleID returns the ticket id of a bundle request path. The id holds a slash, which a
// ServeMux wildcard cannot be followed by "/bundle" after, so the route is the /v1/tickets/ subtree.
func bundleID(p string) (string, bool) {
	id, ok := strings.CutSuffix(strings.TrimPrefix(p, bundleTicketsPrefix), bundleSuffix)
	if !ok || !strings.HasPrefix(p, bundleTicketsPrefix) || !catalog.ValidTicketID(id) {
		return "", false
	}
	return id, true
}

// isBundleRequest reports whether the request takes the per-learner bundle budget.
func isBundleRequest(r *http.Request) bool {
	_, ok := bundleID(r.URL.Path)
	return ok && r.Method == http.MethodGet
}

func (b *Bundles) fetch(w http.ResponseWriter, r *http.Request) {
	id, ok := bundleID(r.URL.Path)
	if !ok {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	row, err := b.Store.GetTicketBundle(r.Context(), id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, http.StatusNotFound, "not_found")
		return
	case err != nil:
		readFailed(w, r, err)
		return
	case !row.BundleKey.Valid || !row.BundleSha256.Valid || !row.BundleBytes.Valid:
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	expires := time.Now().Add(bundleTTL).UTC()
	url, err := b.Signer.PresignGet(r.Context(), row.BundleKey.String, bundleTTL)
	if err != nil {
		if r.Context().Err() == nil {
			slog.Error("bundle presign failed", "item", id, "error", err)
			writeError(w, http.StatusInternalServerError, "server_error")
		}
		return
	}
	writeNoStore(w, bundleResponse{
		ItemID: row.ID, CatalogVersion: row.CatalogVersion, SHA256: row.BundleSha256.String,
		Bytes: row.BundleBytes.Int32, URL: url, ExpiresAt: expires.Format(time.RFC3339),
	})
}
