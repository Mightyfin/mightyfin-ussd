package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Mightyfin/mightyfin-ussd/internal/lmsbridge"
)

func privateFile(path string) ([]byte, error) {
	info, e := os.Stat(path)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("credential files must be private regular files")
	}
	return os.ReadFile(path)
}
func run() error {
	if os.Getenv("USSD_LMS_ENABLED") != "true" {
		return errors.New("LMS adapter is disabled")
	}
	addr := os.Getenv("USSD_LMS_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:28091"
	}
	host, _, e := net.SplitHostPort(addr)
	if e != nil || host != "127.0.0.1" {
		return errors.New("adapter must listen on loopback")
	}
	client, e := lmsbridge.NewClient(os.Getenv("USSD_LMS_BASE_URL"))
	if e != nil {
		return e
	}
	serviceKey, e := privateFile(os.Getenv("USSD_LMS_SERVICE_KEY_FILE"))
	if e != nil {
		return e
	}
	if len(strings.TrimSpace(string(serviceKey))) < 32 {
		return errors.New("service key is too short")
	}
	data := os.Getenv("USSD_LMS_DATA_DIR")
	if !filepath.IsAbs(data) {
		return errors.New("absolute adapter data directory required")
	}
	if e = os.MkdirAll(data, 0700); e != nil {
		return e
	}
	key, e := privateFile(os.Getenv("USSD_LMS_JOURNAL_KEY_FILE"))
	if e != nil {
		return e
	}
	journal, e := lmsbridge.OpenJournal(filepath.Join(data, "requests.enc"), key)
	if e != nil {
		return e
	}
	handler := &lmsbridge.Server{Client: client, Journal: journal, ServiceKey: strings.TrimSpace(string(serviceKey)), Writes: os.Getenv("USSD_LMS_WRITES_ENABLED") == "true", Payments: os.Getenv("USSD_LMS_PAYMENTS_ENABLED") == "true"}
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16384}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("USSD LMS adapter on %s; application writes=%t payments=%t", addr, handler.Writes, handler.Payments)
	e = server.ListenAndServe()
	if e == http.ErrServerClosed {
		return nil
	}
	return e
}
func main() {
	if e := run(); e != nil {
		log.Fatal(e)
	}
}
