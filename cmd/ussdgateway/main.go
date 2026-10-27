package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Mightyfin/mightyfin-ussd/internal/ussdgateway"
)

func keyFile(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("private regular credential file required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	key := strings.TrimSpace(string(data))
	if len(key) < 32 {
		return "", errors.New("invalid credential")
	}
	return key, nil
}

func run() error {
	operatorKey, err := keyFile(os.Getenv("USSD_GATEWAY_KEY_FILE"))
	if err != nil {
		return err
	}
	bridgeKey, err := keyFile(os.Getenv("USSD_LMS_SERVICE_KEY_FILE"))
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	handler := &ussdgateway.Server{Key: operatorKey, Reader: &ussdgateway.BridgeReader{Key: bridgeKey, HTTP: client}}
	server := &http.Server{Addr: "127.0.0.1:28093", Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 6 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Print("USSD proposed-contract gateway listening on 127.0.0.1:28093; read-only pilot")
	err = server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}
func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
