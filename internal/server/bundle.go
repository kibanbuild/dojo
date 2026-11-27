package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/kibanyadev/dojo/internal/server/repo"
)

const (
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

// isTicketRequest reports whether the request is for the given route of the /v1/tickets/ subtree.
func isTicketRequest(r *http.Request, want ticketRoute) bool {
	_, route, _ := parseTicketPath(r.URL.Path)
	return route == want
}

func (b *Bundles) fetch(w http.ResponseWriter, r *http.Request) {
	id, route, _ := parseTicketPath(r.URL.Path)
	if route != routeBundle {
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
			slog.Error("bundle presign failed", "item", row.ID, "error", err)
			writeError(w, http.StatusInternalServerError, "server_error")
		}
		return
	}
	writeNoStore(w, bundleResponse{
		ItemID: row.ID, CatalogVersion: row.CatalogVersion, SHA256: row.BundleSha256.String,
		Bytes: row.BundleBytes.Int32, URL: url, ExpiresAt: expires.Format(time.RFC3339),
	})
}
