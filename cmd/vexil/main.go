// Command vexil runs the uptime monitor.
//
//	vexil                 start the server
//	vexil reset-password  set a new admin password and log out all sessions
//	vexil healthcheck     ask /readyz and exit with 0 or 1, for Docker
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	"github.com/InAtTheGeekEnd/vexil/internal/config"
	"github.com/InAtTheGeekEnd/vexil/internal/engine"
	"github.com/InAtTheGeekEnd/vexil/internal/notify"
	"github.com/InAtTheGeekEnd/vexil/internal/store"
	"github.com/InAtTheGeekEnd/vexil/internal/web"
	"golang.org/x/term"
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	slog.SetDefault(log)

	cfg := config.Load(os.LookupEnv)

	var err error
	switch cmd := arg(1); cmd {
	case "":
		err = serve(cfg, log)
	case "reset-password":
		err = resetPassword(cfg)
	case "healthcheck":
		err = healthcheck(cfg)
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func arg(i int) string {
	if len(os.Args) > i {
		return os.Args[i]
	}
	return ""
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage:\n  %[1]s                 start the server\n  %[1]s reset-password  set a new admin password\n  %[1]s healthcheck     exit 0 when /readyz answers ok\n\nEnvironment:\n  VEXIL_ADDR      listen address (default :8080)\n  VEXIL_DATA      data folder (default ./data)\n  VEXIL_BASE_URL  public URL used in notification links\n", brand.Default.Name)
}

func serve(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.Data)
	if err != nil {
		return err
	}
	defer st.Close()

	notifier := notify.NewService(st, notify.Options{Log: log, Brand: brand.Default.Name, BaseURL: cfg.BaseURL})
	eng := engine.New(st, engine.Options{Log: log, Notifier: notifier})
	if err := eng.Start(ctx); err != nil {
		return err
	}
	defer eng.Stop()

	// The retention job stops before the engine and the store close.
	jobCtx, stopJobs := context.WithCancel(ctx)
	retentionDone := st.RunRetention(jobCtx, log)
	defer func() {
		stopJobs()
		<-retentionDone
	}()

	srv, err := web.New(st, web.Options{Log: log, Engine: eng, BaseURL: cfg.BaseURL, Notifier: notifier})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// SSE streams are open requests. End them first so Shutdown does not
	// wait for them.
	httpSrv.RegisterOnShutdown(srv.CloseEvents)

	errc := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "data", cfg.Data)
		errc <- httpSrv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

func resetPassword(cfg config.Config) error {
	ctx := context.Background()
	st, err := store.Open(ctx, cfg.Data)
	if err != nil {
		return err
	}
	defer st.Close()

	in := bufio.NewReader(os.Stdin)
	password, err := prompt(in, "New password: ")
	if err != nil {
		return err
	}
	confirm, err := prompt(in, "Repeat password: ")
	if err != nil {
		return err
	}
	if err := web.ValidateNewPassword(password, confirm); err != nil {
		return err
	}
	hash, err := web.HashPassword(password)
	if err != nil {
		return err
	}
	if err := st.SetPasswordHash(ctx, hash, ""); err != nil {
		return err
	}
	fmt.Println("Password updated. All sessions were logged out.")
	return nil
}

// healthcheck asks the running server for /readyz. Docker calls it, as the
// distroless image has no curl.
func healthcheck(cfg config.Config) error {
	host, port, err := net.SplitHostPort(cfg.Addr)
	if err != nil {
		return fmt.Errorf("bad listen address %q: %w", cfg.Addr, err)
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Get("http://" + net.JoinHostPort(host, port) + "/readyz")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("not ready: %s", strings.TrimSpace(string(body)))
	}
	fmt.Println(strings.TrimSpace(string(body)))
	return nil
}

// prompt reads one line from stdin. On a terminal it hides the typed text.
func prompt(in *bufio.Reader, label string) (string, error) {
	fmt.Print(label)
	fd := int(os.Stdin.Fd())
	if term.IsTerminal(fd) {
		b, err := term.ReadPassword(fd)
		fmt.Println()
		if err != nil {
			return "", fmt.Errorf("read input: %w", err)
		}
		return string(b), nil
	}
	line, err := in.ReadString('\n')
	if err != nil && line == "" {
		return "", fmt.Errorf("read input: %w", err)
	}
	return strings.TrimRight(line, "\r\n"), nil
}
