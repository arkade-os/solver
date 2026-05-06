package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
)

const (
	defaultDatadir         = ".bancod"
	defaultGRPCPort        = 7070
	defaultHTTPPort        = 7071
	defaultLogLevel        = 4 // logrus.InfoLevel
	defaultBancoEnabled    = true
	defaultPreimageEnabled = false
)

// Config holds all configuration for the bancod server.
type Config struct {
	Datadir         string
	ArkURL          string
	WalletSeed      string
	WalletPassword  string
	IntrospectorURL string
	GRPCPort        int
	HTTPPort        int
	LogLevel        int

	// Plugin toggles. At least one must be enabled.
	BancoEnabled    bool
	PreimageEnabled bool
}

// LoadConfig reads BANCOD_* environment variables and returns a Config
// with defaults applied for optional values.
func LoadConfig() (*Config, error) {
	arkURL := os.Getenv("BANCOD_ARK_URL")
	if arkURL == "" {
		return nil, fmt.Errorf("BANCOD_ARK_URL is required")
	}

	walletSeed := os.Getenv("BANCOD_WALLET_SEED")
	if walletSeed == "" {
		return nil, fmt.Errorf("BANCOD_WALLET_SEED is required")
	}

	introspectorURL := os.Getenv("BANCOD_INTROSPECTOR_URL")
	if introspectorURL == "" {
		return nil, fmt.Errorf("BANCOD_INTROSPECTOR_URL is required")
	}

	datadir := os.Getenv("BANCOD_DATADIR")
	if datadir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("failed to determine home directory: %w", err)
		}
		datadir = filepath.Join(home, defaultDatadir)
	}

	walletPassword := os.Getenv("BANCOD_WALLET_PASSWORD")

	grpcPort := defaultGRPCPort
	if v := os.Getenv("BANCOD_GRPC_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid BANCOD_GRPC_PORT: %w", err)
		}
		grpcPort = p
	}

	httpPort := defaultHTTPPort
	if v := os.Getenv("BANCOD_HTTP_PORT"); v != "" {
		p, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid BANCOD_HTTP_PORT: %w", err)
		}
		httpPort = p
	}

	logLevel := defaultLogLevel
	if v := os.Getenv("BANCOD_LOG_LEVEL"); v != "" {
		l, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("invalid BANCOD_LOG_LEVEL: %w", err)
		}
		logLevel = l
	}

	if grpcPort < 1 || grpcPort > 65535 {
		return nil, fmt.Errorf("BANCOD_GRPC_PORT must be between 1 and 65535")
	}
	if httpPort < 1 || httpPort > 65535 {
		return nil, fmt.Errorf("BANCOD_HTTP_PORT must be between 1 and 65535")
	}
	if grpcPort == httpPort {
		return nil, fmt.Errorf("BANCOD_GRPC_PORT and BANCOD_HTTP_PORT must be different")
	}

	bancoEnabled, err := parseBool("BANCOD_BANCO_ENABLED", defaultBancoEnabled)
	if err != nil {
		return nil, err
	}
	preimageEnabled, err := parseBool("BANCOD_PREIMAGE_ENABLED", defaultPreimageEnabled)
	if err != nil {
		return nil, err
	}
	if !bancoEnabled && !preimageEnabled {
		return nil, fmt.Errorf("at least one plugin must be enabled (BANCOD_BANCO_ENABLED or BANCOD_PREIMAGE_ENABLED)")
	}

	return &Config{
		Datadir:         datadir,
		ArkURL:          arkURL,
		WalletSeed:      walletSeed,
		WalletPassword:  walletPassword,
		IntrospectorURL: introspectorURL,
		GRPCPort:        grpcPort,
		HTTPPort:        httpPort,
		LogLevel:        logLevel,
		BancoEnabled:    bancoEnabled,
		PreimageEnabled: preimageEnabled,
	}, nil
}

// parseBool reads an env var as "true"/"false" (case-insensitive). Empty → fallback.
func parseBool(key string, fallback bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return fallback, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid %s: %w (expected true/false)", key, err)
	}
	return b, nil
}
