package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"ordermatch/internal/api"
	"ordermatch/internal/engine"
	"ordermatch/internal/metrics"
)

func main() {
	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}

	m := metrics.New()
	eng := engine.NewEngine()
	server := api.NewServer(eng, m)
	httpServer := api.NewHTTPServer(addr, server)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := api.Run(ctx, httpServer); err != nil {
		log.Fatalf("server error: %v", err)
	}
}
