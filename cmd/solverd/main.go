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

var Version = "dev"

func main() {
	log := logrus.New()

	if err := run(log); err != nil {
		log.Error(err)
		os.Exit(1)
	}
}

func run(log *logrus.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log.SetLevel(logrus.Level(cfg.LogLevel))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	log.WithField("version", Version).
		Info("starting solverd")

	wallet, err := application.SetupWallet(ctx, cfg)
	if err != nil {
		return err
	}
	defer wallet.Stop()

	log.Info("wallet initialized")

	svc, err := application.New(cfg, wallet)
	if err != nil {
		return err
	}
	defer svc.Close()

	srv := grpcservice.NewServer(cfg.GRPCPort, cfg.HTTPPort, svc)
	if err := srv.Start(); err != nil {
		return err
	}
	defer srv.Stop()

	if err := svc.Run(ctx); err != nil {
		return err
	}
	log.Info("solverd stopped")

	return nil
}
