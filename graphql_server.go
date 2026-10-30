package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/99designs/gqlgen/graphql"
	"github.com/99designs/gqlgen/graphql/handler"
	"github.com/99designs/gqlgen/graphql/handler/extension"
	"github.com/99designs/gqlgen/graphql/handler/lru"
	"github.com/99designs/gqlgen/graphql/handler/transport"
	"github.com/vektah/gqlparser/v2/ast"
)

const (
	graphqlPath    = "/query"
	graphqlMaxBody = 1 << 20
)

// runServer starts a GraphQL HTTP endpoint for the same schema the inline
// commands use. Requests execute independently and are not transactional.
func runServer(args []string, out io.Writer, addr, password string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(out)
	listen := fs.String("listen", "127.0.0.1:8080", "HTTP listen address (HOST:PORT)")
	fs.Usage = func() {
		fmt.Fprintln(out, "Usage: factorio-ai-player serve [--listen HOST:PORT]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("serve takes no positional arguments")
	}
	if password == "" {
		return errors.New("FACTORIO_RCON_PASSWORD is required")
	}
	if strings.TrimSpace(*listen) == "" {
		return errors.New("--listen address is required")
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return fmt.Errorf("listen %s: %w", *listen, err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return serveGraphQL(ctx, out, newGraphQLSchema(addr, password), listener)
}

// serveGraphQL serves the schema until ctx is cancelled or the server fails.
func serveGraphQL(ctx context.Context, out io.Writer, schema graphql.ExecutableSchema, listener net.Listener) error {
	mux := http.NewServeMux()
	mux.Handle(graphqlPath, newGraphQLHandler(schema))
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	fmt.Fprintf(out, "GraphQL endpoint: http://%s%s\n", listener.Addr(), graphqlPath)
	served := make(chan error, 1)
	go func() { served <- server.Serve(listener) }()
	select {
	case err := <-served:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	if err := <-served; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// newGraphQLHandler exposes only HTTP GET/POST/OPTIONS, with introspection and
// a bounded request body. GET accepts query operations only.
func newGraphQLHandler(schema graphql.ExecutableSchema) http.Handler {
	srv := handler.New(schema)
	srv.AddTransport(transport.Options{})
	srv.AddTransport(transport.GET{})
	srv.AddTransport(transport.POST{})
	srv.SetQueryCache(lru.New[*ast.QueryDocument](1000))
	srv.Use(extension.Introspection{})
	return http.MaxBytesHandler(srv, graphqlMaxBody)
}
