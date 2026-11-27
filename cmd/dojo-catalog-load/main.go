// Command dojo-catalog-load is the one-off catalog load (design 3.8): it reads a catalog checkout,
// uploads the ticket bundles and upserts the catalog tables with the migration role.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kibanyadev/dojo/internal/catalogload"
	"github.com/kibanyadev/dojo/internal/dbpool"
	"github.com/kibanyadev/dojo/internal/storage"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, os.Stdout); err != nil {
		slog.Error("dojo-catalog-load failed", "error", err)
		os.Exit(1)
	}
}

// run never falls back to a service role: CATALOG_DATABASE_URL is the migration role's string.
func run(ctx context.Context, args []string, getenv func(string) string, out io.Writer) error {
	fs := flag.NewFlagSet("dojo-catalog-load", flag.ContinueOnError)
	dir := fs.String("catalog", "/catalog", "catalog checkout directory")
	commit := fs.String("version", "", "catalog repo commit hash (at least 12 hex characters)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	version, err := catalogload.Version(*commit)
	if err != nil {
		return err
	}
	dsn := getenv("CATALOG_DATABASE_URL")
	if dsn == "" {
		return errors.New("CATALOG_DATABASE_URL is required: the migration role's connection string")
	}
	cfg, err := storage.LoadConfig(getenv, getenv("DOJO_ENV") == "local")
	if err != nil {
		return err
	}
	store, err := storage.New(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	pool, err := dbpool.New(ctx, dsn)
	if err != nil {
		return fmt.Errorf("open pool: %w", err)
	}
	defer pool.Close()

	res, err := catalogload.Load(ctx, pool, store, os.DirFS(*dir), version)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "catalog %s loaded: %d tickets, %d bundles uploaded, %d reused\n", version, res.Tickets, res.Uploaded, res.Reused)
	return nil
}
