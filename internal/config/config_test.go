package config

import (
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

// setRequiredEnv sets the three mandatory SOLVER_* vars and resets viper's
// global state so each case starts clean.
func setRequiredEnv(t *testing.T) {
	t.Helper()
	viper.Reset()
	t.Cleanup(viper.Reset)
	t.Setenv("SOLVER_ARK_URL", "localhost:7000")
	t.Setenv("SOLVER_WALLET_SEED", "deadbeef")
	t.Setenv("SOLVER_EMULATOR_URL", "localhost:6000")
}

func TestLoadConfig_Defaults(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := LoadConfig()
	require.NoError(t, err)

	require.Equal(t, "localhost:7000", cfg.ArkURL)
	require.Equal(t, "deadbeef", cfg.WalletSeed)
	require.Equal(t, "localhost:6000", cfg.EmulatorURL)
	require.Equal(t, defaultGRPCPort, cfg.GRPCPort)
	require.Equal(t, defaultHTTPPort, cfg.HTTPPort)
	require.Equal(t, defaultLogLevel, cfg.LogLevel)
	require.Empty(t, cfg.WalletPassword)

	home, err := defaultDatadirPath()
	require.NoError(t, err)
	require.Equal(t, home, cfg.Datadir)
	require.Equal(t, defaultDatadirName, filepath.Base(cfg.Datadir))
}

func TestLoadConfig_EnvOverrides(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SOLVER_DATADIR", "/tmp/custom-solverd")
	t.Setenv("SOLVER_WALLET_PASSWORD", "hunter2")
	t.Setenv("SOLVER_GRPC_PORT", "9090")
	t.Setenv("SOLVER_HTTP_PORT", "9091")
	t.Setenv("SOLVER_LOG_LEVEL", "5")

	cfg, err := LoadConfig()
	require.NoError(t, err)

	require.Equal(t, "/tmp/custom-solverd", cfg.Datadir)
	require.Equal(t, "hunter2", cfg.WalletPassword)
	require.Equal(t, 9090, cfg.GRPCPort)
	require.Equal(t, 9091, cfg.HTTPPort)
	require.Equal(t, 5, cfg.LogLevel)
}

func TestLoadConfig_RequiredFields(t *testing.T) {
	tests := []struct {
		name  string
		unset string
		want  string
	}{
		{"missing ark url", "SOLVER_ARK_URL", "ARK_URL is required"},
		{"missing wallet seed", "SOLVER_WALLET_SEED", "WALLET_SEED is required"},
		{"missing emulator url", "SOLVER_EMULATOR_URL", "EMULATOR_URL is required"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(tc.unset, "")

			_, err := LoadConfig()
			require.ErrorContains(t, err, tc.want)
		})
	}
}

func TestLoadConfig_ExplorerURL(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("SOLVER_EXPLORER_URL", "http://chopsticks:3000")

	cfg, err := LoadConfig()
	require.NoError(t, err)
	require.Equal(t, "http://chopsticks:3000", cfg.ExplorerURL)
}

func TestLoadConfig_ExplorerURL_OptionalEmpty(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := LoadConfig()
	require.NoError(t, err)
	require.Empty(t, cfg.ExplorerURL)
}

func TestLoadConfig_PortValidation(t *testing.T) {
	tests := []struct {
		name string
		grpc string
		http string
		want string
	}{
		{"grpc out of range", "0", "7071", "GRPC_PORT must be between 1 and 65535"},
		{"http out of range", "7070", "70000", "HTTP_PORT must be between 1 and 65535"},
		{"same port", "8080", "8080", "must be different"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("SOLVER_GRPC_PORT", tc.grpc)
			t.Setenv("SOLVER_HTTP_PORT", tc.http)

			_, err := LoadConfig()
			require.ErrorContains(t, err, tc.want)
		})
	}
}
