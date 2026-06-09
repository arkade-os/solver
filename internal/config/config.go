package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/viper"
)

const defaultDatadirName = ".solverd"

// Environment variable keys, without the SOLVER_ prefix that viper prepends
// (e.g. ArkURL is read from SOLVER_ARK_URL).
var (
	Datadir        = "DATADIR"
	ArkURL         = "ARK_URL"
	ExplorerURL    = "EXPLORER_URL"
	WalletSeed     = "WALLET_SEED"
	WalletPassword = "WALLET_PASSWORD"
	EmulatorURL    = "EMULATOR_URL"
	GRPCPort       = "GRPC_PORT"
	HTTPPort       = "HTTP_PORT"
	LogLevel       = "LOG_LEVEL"
)

const (
	defaultGRPCPort = 7070
	defaultHTTPPort = 7071
	defaultLogLevel = 4 // logrus.InfoLevel
)

// Config holds all configuration for the solverd server.
type Config struct {
	Datadir        string
	ArkURL         string
	ExplorerURL    string
	WalletSeed     string
	WalletPassword string
	EmulatorURL    string
	GRPCPort       int
	HTTPPort       int
	LogLevel       int
}

// LoadConfig reads SOLVER_* environment variables via viper and returns a
// Config with defaults applied for optional values.
func LoadConfig() (*Config, error) {
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
		ExplorerURL:    viper.GetString(ExplorerURL),
		WalletSeed:     walletSeed,
		WalletPassword: viper.GetString(WalletPassword),
		EmulatorURL:    emulatorURL,
		GRPCPort:       grpcPort,
		HTTPPort:       httpPort,
		LogLevel:       viper.GetInt(LogLevel),
	}, nil
}

// defaultDatadirPath returns $HOME/.solverd, the default data directory.
func defaultDatadirPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to determine home directory: %w", err)
	}
	return filepath.Join(home, defaultDatadirName), nil
}
