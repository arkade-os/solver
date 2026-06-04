package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/sirupsen/logrus"

	"github.com/arkade-os/solver/internal/config"
	"github.com/arkade-os/solver/internal/core/application"
	grpcservice "github.com/arkade-os/solver/internal/interface/grpc"
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
		Info("starting solverd")

	wallet, err := application.SetupWallet(ctx, cfg)
	if err != nil {
		return err
	}
	defer wallet.Stop()

	svc, err := application.New(cfg, log, wallet)
	if err != nil {
		return err
	}

	srv := grpcservice.NewServer(cfg.GRPCPort, cfg.HTTPPort, svc)

	if err := svc.Run(ctx, srv); err != nil {
		return err
	}
	log.Info("solverd stopped")

	return nil
}
