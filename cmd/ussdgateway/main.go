package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Mightyfin/mightyfin-ussd/internal/nyali"
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

func privateFile(path string) ([]byte, error) {
	info, e := os.Lstat(path)
	if e != nil {
		return nil, e
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("private regular configuration file required")
	}
	return os.ReadFile(path)
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
	if ip := os.Getenv("USSD_HOBBITON_ALLOWED_IP"); ip != "" {
		parsed, e := netip.ParseAddr(ip)
		if e != nil || !parsed.Is4() {
			return errors.New("invalid Hobbiton gateway IPv4 address")
		}
		handler.HobbitonIP = parsed.String()
	}
	if os.Getenv("USSD_PAYMENTS_ENABLED") == "true" {
		paymentKey, e := keyFile(os.Getenv("USSD_PAYMENTS_KEY_FILE"))
		if e != nil {
			return e
		}
		handler.Payments = &ussdgateway.PaymentClient{Key: paymentKey, HTTP: client}
	}
	if path := os.Getenv("USSD_LEADS_KEY_FILE"); path != "" {
		key, e := keyFile(path)
		if e != nil {
			return e
		}
		handler.Leads = &ussdgateway.LeadClient{PaymentClient: ussdgateway.PaymentClient{Key: key, HTTP: client}}
	}
	var nyaliService *ussdgateway.NyaliPayments
	switch os.Getenv("USSD_REPAYMENT_BACKEND") {
	case "", "local-lms":
	case "nyali":
		token, e := privateFile(os.Getenv("USSD_NYALI_TOKEN_FILE"))
		if e != nil {
			return e
		}
		api, e := nyali.New(strings.TrimSpace(string(token)), client)
		if e != nil {
			return e
		}
		secret, e := privateFile(os.Getenv("USSD_NYALI_JOURNAL_KEY_FILE"))
		if e != nil {
			return e
		}
		path := os.Getenv("USSD_NYALI_JOURNAL_FILE")
		if path == "" {
			return errors.New("Nyali journal path required")
		}
		store, e := nyali.OpenStore(path, secret)
		if e != nil {
			return e
		}
		defer store.Close()
		nyaliService = &ussdgateway.NyaliPayments{Client: api, Store: store}
		if fallback, ok := handler.Payments.(interface {
			Welcome(context.Context, string) (string, error)
		}); ok {
			nyaliService.Fallback = fallback
		}
		handler.Payments = nyaliService
	default:
		return errors.New("unsupported repayment backend")
	}
	server := &http.Server{Addr: "127.0.0.1:28093", Handler: handler, ReadHeaderTimeout: 3 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 6 * time.Second, IdleTimeout: 15 * time.Second, MaxHeaderBytes: 8192}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if nyaliService != nil {
		go nyaliService.Run(ctx)
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 6*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("USSD proposed-contract gateway listening on 127.0.0.1:28093; payments=%t leads=%t", handler.Payments != nil, handler.Leads != nil)
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
