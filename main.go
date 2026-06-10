package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"
)

func main() {
	ctx, cancel := context.WithCancel(
		context.Background(),
	)
	defer cancel()

	env, err := loadEnv()
	if err != nil {
		log.Fatal(err)
	}

	store, err := OpenStore(ctx, env)
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()

	if err := store.MigrateProduction(ctx); err != nil {
		log.Fatal(err)
	}

	if err := store.SeedProductionDefaults(ctx); err != nil {
		log.Fatal(err)
	}

	app := &App{
		env:           env,
		store:         store,
		client:        &http.Client{Timeout: 25 * time.Second},
		paused:        false,
		lastAction:    "Booting...",
		modelCooldown: make(map[string]time.Time),
	}

	if err := app.reloadConfig(ctx); err != nil {
		log.Fatal(err)
	}

	position, err := store.LoadOpenPosition(ctx)
	if err == nil && position != nil {
		app.position = position
		app.lastAction = "Open position restored from Turso."
	}

	go app.telegramPoller(ctx)
	go app.scannerLoop(ctx)
	go app.positionLoop(ctx)
	go app.dashboardLoop(ctx)

	mux := http.NewServeMux()

	mux.HandleFunc(
		"/healthz",
		app.handleHealthz,
	)

	mux.HandleFunc(
		"/",
		app.handleRoot,
	)

	server := &http.Server{
		Addr:              ":" + env.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	log.Printf(
		"%s started on :%s",
		AppName,
		env.Port,
	)

	if err := server.ListenAndServe(); err != nil &&
		!errors.Is(err, http.ErrServerClosed) {

		log.Fatal(err)
	}
}
