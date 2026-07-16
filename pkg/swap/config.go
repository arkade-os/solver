package swap

import (
	"time"

	emulatorclient "github.com/arkade-os/emulator/pkg/client"
	arksdk "github.com/arkade-os/go-sdk"
	"github.com/sirupsen/logrus"
)

const (
	defaultPriceCacheTTL = 5 * time.Minute
)

type Config struct {
	SolverClient      arksdk.Wallet
	Emulator          emulatorclient.TransportClient
	MarketsRepository MarketRepository
	PriceFeed         PriceFeed
	PriceCacheTTL     time.Duration
	Listener          FulfillmentListener
	Log               logrus.FieldLogger
}

func (cfg Config) WithDefault() Config {
	if cfg.PriceCacheTTL == 0 {
		cfg.PriceCacheTTL = defaultPriceCacheTTL
	}
	if cfg.Log == nil {
		defaultLogger := logrus.New()
		defaultLogger.SetLevel(logrus.InfoLevel)

		cfg.Log = defaultLogger
	}

	return cfg
}
