package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/Mightyfin/mightyfin-ussd/internal/app"
)

func main() {
	dir := os.Getenv("USSD_DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	service, err := app.Open(dir)
	if err != nil {
		log.Fatal(err)
	}
	defer service.Close()
	server := &http.Server{Addr: "127.0.0.1:8090", Handler: service.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Print("MightyFin USSD demo: http://127.0.0.1:8090 (local simulation; no external connections)")
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Print(err)
	}
}
