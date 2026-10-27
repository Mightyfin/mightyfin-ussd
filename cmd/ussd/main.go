package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Mightyfin/mightyfin-ussd/internal/app"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	addr := os.Getenv("USSD_HTTP_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8090"
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" {
		return fmt.Errorf("USSD_HTTP_ADDR must bind to 127.0.0.1:port")
	}
	dir := os.Getenv("USSD_DATA_DIR")
	if dir == "" {
		dir = "data"
	}
	service, err := app.Open(dir)
	if err != nil {
		return err
	}
	defer service.Close()
	server := &http.Server{Addr: addr, Handler: service.HandlerForAddress(addr), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(ctx)
	}()
	log.Printf("MightyFin USSD demo: http://%s (local simulation; no external connections)", addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
