package swap

import (
	emulatorclient "github.com/arkade-os/emulator/pkg/client"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/sirupsen/logrus"
)

type Config struct {
	SolverClient      arksdk.Wallet
	Emulator          emulatorclient.TransportClient
	MarketsRepository MarketRepository
	Listener          AttemptListener
	Log               logrus.FieldLogger
}

func (cfg Config) WithDefault() Config {
	if cfg.Log == nil {
		defaultLogger := logrus.New()
		defaultLogger.SetLevel(logrus.InfoLevel)

		cfg.Log = defaultLogger
	}

	return cfg
}
