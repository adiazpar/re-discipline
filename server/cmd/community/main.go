package main

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/adiazpar/re-discipline/retrieval/buildinfo"
	"github.com/adiazpar/re-discipline/server"
	"github.com/adiazpar/re-discipline/server/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("service stopped", "error", err)
		os.Exit(1)
	}
}
func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	if command == "version" {
		return json.NewEncoder(os.Stdout).Encode(buildinfo.Current())
	}
	setupOnly := command == "serve-setup"
	localOnly := command == "serve-local"
	if command != "serve" && !setupOnly && !localOnly && command != "migrate" && command != "bootstrap-runtime" {
		return errors.New("usage: community [serve|serve-setup|serve-local|migrate|bootstrap-runtime]")
	}
	poolConfig, err := pgxpool.ParseConfig(os.Getenv("DATABASE_URL"))
	if err != nil {
		return errors.New("DATABASE_URL is not a valid PostgreSQL connection configuration")
	}
	server.ConfigureMemory()
	budget := server.CurrentSearchBudget()
	poolConfig.MaxConns = int32(budget.DBConnections)
	if command == "serve" || setupOnly || localOnly {
		poolConfig.AfterConnect = func(ctx context.Context, c *pgx.Conn) error {
			_, err := c.Exec(ctx, `SET ROLE re_community_worker`)
			return err
		}
	}
	db, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return err
	}
	defer db.Close()
	if command == "migrate" {
		return database.Migrate(ctx, db)
	}
	if command == "bootstrap-runtime" {
		f, err := os.OpenFile(".env.runtime", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		dsn, err := database.ProvisionRuntime(ctx, db, os.Getenv("DATABASE_URL"))
		if err != nil {
			f.Close()
			os.Remove(".env.runtime")
			return err
		}
		defer f.Close()
		_, err = f.WriteString("DATABASE_URL=" + dsn + "\n")
		if err == nil {
			slog.Info("Runtime connection saved to .env.runtime; keep this file private.")
		}
		return err
	}
	if err = db.Ping(ctx); err != nil {
		return err
	}
	public := env("PUBLIC_URL", env("RENDER_EXTERNAL_URL", "http://localhost:8080"))
	u, err := url.Parse(public)
	if err != nil {
		return err
	}
	if u.Scheme != "https" && u.Hostname() != "localhost" && u.Hostname() != "127.0.0.1" {
		return errors.New("PUBLIC_URL must use HTTPS outside localhost")
	}
	if u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("PUBLIC_URL must be an HTTP(S) origin without credentials, query, or path")
	}
	cfg := server.Config{PublicURL: strings.TrimRight(public, "/"), CacheDir: env("CACHE_DIR", ".data/search")}
	cfg.TrustedProxy = os.Getenv("TRUSTED_PROXY_IP")
	cfg.OpsStatusDir = os.Getenv("OPS_STATUS_DIR")
	cfg.JevEnabled = os.Getenv("JEV_ENABLED") == "true"
	cfg.OperatorID = os.Getenv("SERVER_OPERATOR_ID")
	cfg.OperatorToken = os.Getenv("SERVER_OPERATOR_TOKEN")
	cfg.SetupOnly = setupOnly
	cfg.LocalOnly = localOnly
	if localOnly {
		cfg.LocalOwnerID = os.Getenv("LOCAL_OWNER_ID")
		cfg.LocalOwnerToken = os.Getenv("LOCAL_OWNER_TOKEN")
		if err := server.ValidateLocalOwner(cfg); err != nil {
			return err
		}
	}
	if !setupOnly && !localOnly {
		if err := server.ValidateOperator(cfg); err != nil {
			return err
		}
	}
	s := server.New(db, cfg)
	if !setupOnly {
		go s.RunWorker(ctx)
	}
	listen := ":" + env("PORT", "8080")
	if localOnly {
		listen = "127.0.0.1" + listen
	}
	h := &http.Server{Addr: env("LISTEN_ADDR", listen), Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 * 1024}
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 35*time.Second)
		defer cancel()
		h.Shutdown(shutdown)
	}()
	slog.Info("community service listening", "address", h.Addr, "search_budget", budget)
	err = h.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		// ListenAndServe returns as soon as its listener closes, before Shutdown
		// finishes draining. Keep the DB and process alive for active requests.
		<-drained
		return nil
	}
	return err
}
func env(k, fallback string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return fallback
}
