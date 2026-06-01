package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sirupsen/logrus"

	"github.com/arkade-os/solver/internal/config"
	"github.com/arkade-os/solver/internal/solverd"
)

// Version is injected at build time via -ldflags "-X main.Version=<tag>".
// Defaults to "dev" for local builds.
var Version = "dev"

func main() {
	log := logrus.New()

	if err := run(log); err != nil {
		log.Error(err)
		os.Exit(1)
	}
}

func run(log *logrus.Logger) error {
	cfg, err := config.LoadConfig()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.WithField("version", Version).
		WithField("banco", cfg.BancoEnabled).
		WithField("preimage", cfg.PreimageEnabled).
		Info("starting solverd")

	wallet, err := solverd.SetupWallet(ctx, cfg)
	if err != nil {
		return err
	}
	defer wallet.Stop()

	if err := solverd.Run(ctx, cfg, log, wallet); err != nil {
		return err
	}
	log.Info("solverd stopped")

	return nil
}
