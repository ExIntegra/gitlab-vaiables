package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"gitlabvars/internal/app"
	"gitlabvars/internal/demo"
	"gitlabvars/internal/gitlab"
)

func main() {
	cfg, err := app.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	client, err := gitlab.New(cfg.GitLabURL, &http.Client{Timeout: 12 * time.Second})
	if err != nil {
		log.Fatal("invalid GITLAB_URL configuration")
	}
	var demoBackend app.Backend
	if cfg.Demo {
		demoBackend = demo.New()
	}
	application := app.New(cfg, client, demoBackend)
	if err := application.ConfigureTracking(cfg.TrackingFile); err != nil {
		log.Fatal("cannot open tracking storage: ", err)
	}
	server := &http.Server{Addr: cfg.Addr, Handler: application.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				application.PruneExpiredSessions()
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			log.Print("server shutdown timed out")
		}
	}()
	log.Printf("GitLab Variables: %s (listen %s, demo=%t)", cfg.Origin, cfg.Addr, cfg.Demo)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal("HTTP server failed: ", err)
	}
}
