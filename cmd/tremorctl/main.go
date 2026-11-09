// Command tremorctl reads Tremor's experiment history from MongoDB.
package main

import (
	"context"
	"os"
	"time"

	"github.com/Amitreddy14/tremor/internal/cli"
	"github.com/Amitreddy14/tremor/internal/runstore"
	"github.com/Amitreddy14/tremor/internal/runstore/mongostore"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.Deps{
		Open:   open,
		Now:    time.Now,
		Getenv: os.Getenv,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
	}))
}

func open(ctx context.Context, uri string) (runstore.Store, func(), error) {
	st, err := mongostore.New(ctx, uri, mongostore.DefaultDatabase)
	if err != nil {
		return nil, nil, err
	}
	// Fail now with a clear error instead of on the first query.
	if err := st.Ping(ctx); err != nil {
		_ = st.Close(context.Background())
		return nil, nil, err
	}
	return st, func() { _ = st.Close(context.Background()) }, nil
}
