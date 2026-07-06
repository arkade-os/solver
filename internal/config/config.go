package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

type Config struct {
	Datadir        string
	ArkURL         string
	WalletSeed     string
	WalletPassword string
	EmulatorURL    string
	ExplorerURL    string
	GRPCPort       int
	HTTPPort       int
	LogLevel       int
}

var (
	Datadir        = "DATADIR"
	ArkURL         = "ARK_URL"
	WalletSeed     = "WALLET_SEED"
	WalletPassword = "WALLET_PASSWORD"
	EmulatorURL    = "EMULATOR_URL"
	ExplorerURL    = "EXPLORER_URL"
	GRPCPort       = "GRPC_PORT"
	HTTPPort       = "HTTP_PORT"
	LogLevel       = "LOG_LEVEL"
)

const (
	defaultGRPCPort    = 7170
	defaultHTTPPort    = 7171
	defaultLogLevel    = 4 // logrus.InfoLevel
	defaultDatadirName = ".solverd"
)

// Load config from env variables
func Load() (*Config, error) {
	viper.SetEnvPrefix("SOLVER")
	viper.AutomaticEnv()

	defaultDatadir, err := defaultDatadirPath()
	if err != nil {
		return nil, err
	}

	viper.SetDefault(Datadir, defaultDatadir)
	viper.SetDefault(GRPCPort, defaultGRPCPort)
	viper.SetDefault(HTTPPort, defaultHTTPPort)
	viper.SetDefault(LogLevel, defaultLogLevel)

	arkURL := viper.GetString(ArkURL)
	if arkURL == "" {
		return nil, fmt.Errorf("ARK_URL is required")
	}

	walletSeed := viper.GetString(WalletSeed)
	if walletSeed == "" {
		return nil, fmt.Errorf("WALLET_SEED is required")
	}

	emulatorURL := viper.GetString(EmulatorURL)
	if emulatorURL == "" {
		return nil, fmt.Errorf("EMULATOR_URL is required")
	}

	grpcPort := viper.GetInt(GRPCPort)
	httpPort := viper.GetInt(HTTPPort)
	if grpcPort < 1 || grpcPort > 65535 {
		return nil, fmt.Errorf("GRPC_PORT must be between 1 and 65535")
	}
	if httpPort < 1 || httpPort > 65535 {
		return nil, fmt.Errorf("HTTP_PORT must be between 1 and 65535")
	}
	if grpcPort == httpPort {
		return nil, fmt.Errorf("GRPC_PORT and HTTP_PORT must be different")
	}

	return &Config{
		Datadir:        viper.GetString(Datadir),
		ArkURL:         arkURL,
		WalletSeed:     walletSeed,
		WalletPassword: viper.GetString(WalletPassword),
		EmulatorURL:    emulatorURL,
		ExplorerURL:    viper.GetString(ExplorerURL),
		GRPCPort:       grpcPort,
		HTTPPort:       httpPort,
		LogLevel:       viper.GetInt(LogLevel),
	}, nil
}

func defaultDatadirPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine home directory: %w", err)
	}
	return filepath.Join(home, defaultDatadirName), nil
}
