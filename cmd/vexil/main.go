// Command vexil runs the uptime monitor.
//
//	vexil                 start the server
//	vexil reset-password  set a new admin password and log out all sessions
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/InAtTheGeekEnd/vexil/internal/brand"
	"github.com/InAtTheGeekEnd/vexil/internal/config"
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
	fmt.Fprintf(os.Stderr, "Usage:\n  %[1]s                 start the server\n  %[1]s reset-password  set a new admin password\n\nEnvironment:\n  VEXIL_ADDR      listen address (default :8080)\n  VEXIL_DATA      data folder (default ./data)\n  VEXIL_BASE_URL  public URL used in notification links\n", brand.Default.Name)
}

func serve(cfg config.Config, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(ctx, cfg.Data)
	if err != nil {
		return err
	}
	defer st.Close()

	srv, err := web.New(st, web.Options{Log: log})
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

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
	if err := st.SetPasswordHash(ctx, hash); err != nil {
		return err
	}
	fmt.Println("Password updated. All sessions were logged out.")
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
