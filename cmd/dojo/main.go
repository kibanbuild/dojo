// Command dojo runs Kiban Dojo katas on a local lab VM.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/signal"
	"time"

	"github.com/kibanbuild/dojo"
	"github.com/kibanbuild/dojo/internal/cli"
	"github.com/kibanbuild/dojo/internal/lab"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	backend := os.Getenv("DOJO_BACKEND")
	if backend == "" {
		backend = lab.DefaultName()
	}
	app := &cli.App{
		Out:         os.Stdout,
		Catalog:     catalog(),
		BackendName: backend,
		NewBackend:  lab.New,
		Now:         time.Now,
	}
	err := app.Run(ctx, os.Args[1:])
	switch {
	case err == nil:
	case errors.Is(err, cli.ErrCheckFailed):
		os.Exit(1) // already explained on stdout
	default:
		fmt.Fprintln(os.Stderr, "dojo:", err)
		os.Exit(1)
	}
}

func catalog() fs.FS {
	if dir := os.Getenv("DOJO_KATAS"); dir != "" {
		return os.DirFS(dir)
	}
	return dojo.Katas()
}
