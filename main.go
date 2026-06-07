package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
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

	app := &App{
		env:        env,
		store:      store,
		client:     &http.Client{Timeout: 25 * time.Second},
		paused:     false,
		lastAction: "Booting...",
	}

	if err := app.reloadConfig(ctx); err != nil {
		log.Fatal(err)
	}

	pos, err := store.LoadOpenPosition(ctx)
	if err == nil && pos != nil {
		app.position = pos
	}

	go app.telegramPoller(ctx)
	go app.scannerLoop(ctx)
	go app.positionLoop(ctx)
	go app.dashboardLoop(ctx)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", app.handleHealthz)
	mux.HandleFunc("/", app.handleRoot)

	server := &http.Server{
		Addr:              ":" + env.Port,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("%s started on :%s", AppName, env.Port)
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
