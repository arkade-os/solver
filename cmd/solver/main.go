package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/urfave/cli/v2"
)

var (
	Version    string
	httpClient = &http.Client{Timeout: 10 * time.Second}
)

func main() {
	app := &cli.App{
		Name:    "solver",
		Usage:   "solverd CLI - manage the taker bot",
		Version: Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "server",
				Usage:   "solverd HTTP server address",
				Value:   "http://localhost:7071",
				EnvVars: []string{"SOLVER_SERVER"},
			},
		},
		Commands: []*cli.Command{
			pairCommand,
			statusCommand,
			balanceCommand,
			addressCommand,
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "error: %s\n", err)
		os.Exit(1)
	}
}

// --- pair commands ---

var pairCommand = &cli.Command{
	Name:  "pair",
	Usage: "manage trading pairs",
	Subcommands: []*cli.Command{
		{
			Name:  "add",
			Usage: "add a new trading pair",
			Flags: pairFlags(),
			Action: func(c *cli.Context) error {
				pair, err := parsePairFlags(c)
				if err != nil {
					return err
				}
				return doPost(c, "/v1/pair", map[string]any{"pair": pair})
			},
		},
		{
			Name:  "update",
			Usage: "update an existing trading pair",
			Flags: pairFlags(),
			Action: func(c *cli.Context) error {
				pair, err := parsePairFlags(c)
				if err != nil {
					return err
				}
				return doPut(c, "/v1/pair", map[string]any{"pair": pair})
			},
		},
		{
			Name:  "remove",
			Usage: "remove a trading pair",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "pair", Required: true, Usage: "pair name (e.g. BTC/ASSET)"},
			},
			Action: func(c *cli.Context) error {
				pairName := c.String("pair")
				return doDelete(c, "/v1/pair/"+url.PathEscape(pairName))
			},
		},
		{
			Name:  "list",
			Usage: "list all configured trading pairs",
			Action: func(c *cli.Context) error {
				return doGet(c, "/v1/pairs")
			},
		},
	},
}

func pairFlags() []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "pair", Required: true, Usage: "pair name (e.g. BTC/ASSET)"},
		&cli.Uint64Flag{Name: "min", Required: true, Usage: "minimum amount (satoshis)"},
		&cli.Uint64Flag{Name: "max", Required: true, Usage: "maximum amount (satoshis)"},
		&cli.IntFlag{Name: "base-decimals", Value: 0, Usage: "base asset decimal precision"},
		&cli.IntFlag{Name: "quote-decimals", Value: 0, Usage: "quote asset decimal precision"},
		&cli.StringFlag{Name: "price-feed", Required: true, Usage: "price feed URL"},
		&cli.BoolFlag{Name: "invert-price", Usage: "invert the feed price"},
	}
}

func parsePairFlags(c *cli.Context) (map[string]any, error) {
	return map[string]any{
		"pair":           c.String("pair"),
		"min_amount":     c.Uint64("min"),
		"max_amount":     c.Uint64("max"),
		"base_decimals":  c.Int("base-decimals"),
		"quote_decimals": c.Int("quote-decimals"),
		"price_feed":     c.String("price-feed"),
		"invert_price":   c.Bool("invert-price"),
	}, nil
}

// --- simple commands ---

var statusCommand = &cli.Command{
	Name:  "status",
	Usage: "get taker bot status",
	Action: func(c *cli.Context) error {
		return doGet(c, "/v1/status")
	},
}

var balanceCommand = &cli.Command{
	Name:  "balance",
	Usage: "get wallet balance",
	Action: func(c *cli.Context) error {
		return doGet(c, "/v1/balance")
	},
}

var addressCommand = &cli.Command{
	Name:  "address",
	Usage: "get a new wallet address",
	Action: func(c *cli.Context) error {
		return doGet(c, "/v1/address")
	},
}

// --- HTTP helpers ---

func serverURL(c *cli.Context) string {
	return c.String("server")
}

func doGet(c *cli.Context, path string) error {
	resp, err := httpClient.Get(serverURL(c) + path)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	// nolint:errcheck
	defer resp.Body.Close()
	return printResponse(resp)
}

func doPost(c *cli.Context, path string, body any) error {
	return doJSON(c, http.MethodPost, path, body)
}

func doPut(c *cli.Context, path string, body any) error {
	return doJSON(c, http.MethodPut, path, body)
}

func doDelete(c *cli.Context, path string) error {
	req, err := http.NewRequest(http.MethodDelete, serverURL(c)+path, nil)
	if err != nil {
		return err
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	// nolint:errcheck
	defer resp.Body.Close()
	return printResponse(resp)
}

func doJSON(c *cli.Context, method, path string, body any) error {
	jsonBytes, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(method, serverURL(c)+path, bytes.NewReader(jsonBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	// nolint:errcheck
	defer resp.Body.Close()
	return printResponse(resp)
}

func printResponse(resp *http.Response) error {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return fmt.Errorf("server error (%d): %s", resp.StatusCode, string(body))
	}

	// Pretty-print JSON
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, body, "", "  "); err != nil {
		// Not JSON, print raw
		fmt.Println(string(body))
		return nil
	}
	fmt.Println(pretty.String())
	return nil
}
