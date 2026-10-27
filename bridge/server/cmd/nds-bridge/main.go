// Command nds-bridge shows Navidrome playback as Discord statuses.
//
// It receives signed playback events from the Navidrome plugin, serves the
// page where users link their Discord account, and runs one worker process
// per active user that holds the Discord Social SDK connection.
//
// Usage:
//
//	nds-bridge [serve]               run the service
//	nds-bridge links list            list linked accounts (service must be stopped)
//	nds-bridge links remove <user>   revoke and delete a link (service must be stopped)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"text/tabwriter"
	"time"

	"navidrome-discord-status/bridge/internal/config"
	"navidrome-discord-status/bridge/internal/discord"
	"navidrome-discord-status/bridge/internal/events"
	"navidrome-discord-status/bridge/internal/navidrome"
	"navidrome-discord-status/bridge/internal/presence"
	"navidrome-discord-status/bridge/internal/retention"
	"navidrome-discord-status/bridge/internal/store"
	"navidrome-discord-status/bridge/internal/web"
	"navidrome-discord-status/bridge/internal/worker"
)

const (
	shutdownTimeout   = 15 * time.Second
	retentionInterval = 24 * time.Hour
	usage             = "usage: nds-bridge [serve] | nds-bridge links list | nds-bridge links remove <user>"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "nds-bridge:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("configuration: %w", err)
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))

	switch {
	case len(args) == 0 || (len(args) == 1 && args[0] == "serve"):
		return serve(cfg, log)
	case len(args) == 2 && args[0] == "links" && args[1] == "list":
		return listLinks(cfg)
	case len(args) == 3 && args[0] == "links" && args[1] == "remove":
		return removeLink(cfg, args[2])
	default:
		return errors.New(usage)
	}
}

func serve(cfg *config.Config, log *slog.Logger) error {
	for _, warning := range cfg.Warnings() {
		log.Warn(warning)
	}
	links, err := store.Open(cfg.StateDir)
	if err != nil {
		return fmt.Errorf("opening state: %w", err)
	}
	defer links.Close()

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	oauth := discord.NewClient(cfg.DiscordAppID, cfg.DiscordClientSecret, cfg.RedirectURL())
	spawn := func(username, accessToken string) (presence.Worker, error) {
		return worker.Start(cfg.WorkerPath, cfg.DiscordAppID, accessToken, log.With("user", username))
	}
	display := presence.DisplayOptions{
		DefaultImageURL: cfg.DefaultImageURL,
		PauseImageURL:   cfg.PauseImageURL,
		ImageHosts:      cfg.ImageHosts,
		LinkHosts:       cfg.LinkHosts,
	}
	manager := presence.NewManager(ctx, links, oauth, spawn, display, presence.DefaultTiming, log)

	sweeper := &retention.Sweeper{Links: links, Revoker: oauth, Forgetter: manager, Retention: cfg.LinkRetention, Log: log}
	go sweeper.Run(ctx, retentionInterval)

	linkPage := web.NewServer(navidrome.NewAuthenticator(cfg.NavidromeURL), oauth, links, manager, web.Options{
		SecureCookies:  cfg.SecureCookies(),
		TrustedProxies: cfg.TrustedProxies,
		Privacy: web.PrivacyInfo{
			Contact:           cfg.PrivacyContact,
			LinkRetentionDays: int(cfg.LinkRetention / (24 * time.Hour)),
			LogRetentionDays:  cfg.LogRetentionDays,
		},
	}, log)

	servers := []*http.Server{
		newHTTPServer(cfg.EventsAddr, events.NewHandler(cfg.EventSecret, manager.Handle, manager.IsLinked, log)),
		newHTTPServer(cfg.WebAddr, linkPage),
	}
	serveErrs := make(chan error, len(servers))
	for _, srv := range servers {
		log.Info("listening", "addr", srv.Addr)
		go func() { serveErrs <- srv.ListenAndServe() }()
	}

	var serveErr error
	select {
	case <-ctx.Done():
		log.Info("shutting down")
	case serveErr = <-serveErrs:
		stop()
	}
	shutdown(servers, manager, log)
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return serveErr
}

// shutdown stops accepting requests, then waits until every worker has
// cleared its status and exited (the manager's context is already cancelled).
func shutdown(servers []*http.Server, manager *presence.Manager, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	for _, srv := range servers {
		if err := srv.Shutdown(ctx); err != nil {
			log.Warn("server shutdown", "addr", srv.Addr, "error", err)
		}
	}
	manager.Wait()
}

func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
	}
}

func listLinks(cfg *config.Config) error {
	links, err := store.Open(cfg.StateDir)
	if err != nil {
		return err
	}
	defer links.Close()

	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(out, "USER\tDISCORD\tLINKED\tLAST USED")
	for _, l := range links.List() {
		fmt.Fprintf(out, "%s\t%s\t%s\t%s\n", l.Username, l.DiscordName, formatDate(l.LinkedAt), formatDate(l.LastUsed))
	}
	return out.Flush()
}

func removeLink(cfg *config.Config, username string) error {
	links, err := store.Open(cfg.StateDir)
	if err != nil {
		return err
	}
	defer links.Close()

	link, err := links.Get(username)
	if err != nil {
		return err
	}
	oauth := discord.NewClient(cfg.DiscordAppID, cfg.DiscordClientSecret, cfg.RedirectURL())
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := oauth.Revoke(ctx, link.RefreshToken); err != nil {
		fmt.Fprintln(os.Stderr, "warning: revoking the token at Discord failed:", err)
	}
	if err := links.Delete(username); err != nil {
		return err
	}
	fmt.Printf("removed the link of %s\n", username)
	return nil
}

func formatDate(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format("2006-01-02")
}
