package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strconv"
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
			tradesCommand,
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
			Flags: pairFlags(true),
			Action: func(c *cli.Context) error {
				return doPost(c, "/v1/pair", map[string]any{"pair": pairOverrides(c)})
			},
		},
		{
			Name:  "update",
			Usage: "update an existing trading pair (only the given flags change)",
			Flags: pairFlags(false),
			Action: func(c *cli.Context) error {
				current, err := fetchPair(c, c.String("pair"))
				if err != nil {
					return err
				}
				maps.Copy(current, pairOverrides(c))
				return doPut(c, "/v1/pair", map[string]any{"pair": current})
			},
		},
		{
			Name:  "get",
			Usage: "show a single configured trading pair",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "pair", Required: true, Usage: "pair name (e.g. BTC/ASSET)"},
			},
			Action: func(c *cli.Context) error {
				pair, err := fetchPair(c, c.String("pair"))
				if err != nil {
					return err
				}
				return printJSON(pair)
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

// pairFlags returns the flags shared by "pair add" and "pair update". For add
// everything but slippage is required; for update only --pair is, since unset
// flags keep their current value.
func pairFlags(required bool) []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "pair", Required: true, Usage: "pair name (e.g. BTC/ASSET)"},
		&cli.Uint64Flag{Name: "min", Required: required, Usage: "minimum want amount (quote asset base units)"},
		&cli.Uint64Flag{Name: "max", Required: required, Usage: "maximum want amount (quote asset base units)"},
		&cli.StringFlag{Name: "price-feed", Required: required, Usage: "price feed URL"},
		&cli.BoolFlag{Name: "invert-price", Usage: "invert the feed price"},
		&cli.UintFlag{Name: "slippage-bps", Usage: "max price deviation in basis points (default 100 = 1%)"},
	}
}

// pairOverrides returns the request fields for the flags set on the command
// line, keyed by their JSON names.
func pairOverrides(c *cli.Context) map[string]any {
	out := map[string]any{"pair": c.String("pair")}
	if c.IsSet("min") {
		out["min_amount"] = c.Uint64("min")
	}
	if c.IsSet("max") {
		out["max_amount"] = c.Uint64("max")
	}
	if c.IsSet("price-feed") {
		out["price_feed"] = c.String("price-feed")
	}
	if c.IsSet("invert-price") {
		out["invert_price"] = c.Bool("invert-price")
	}
	if c.IsSet("slippage-bps") {
		out["slippage_bps"] = c.Uint("slippage-bps")
	}
	return out
}

// fetchPair returns the named pair's current server-side state as raw JSON
// fields.
func fetchPair(c *cli.Context, name string) (map[string]any, error) {
	var resp struct {
		Pairs []map[string]any `json:"pairs"`
	}
	if err := getJSON(c, "/v1/pairs", &resp); err != nil {
		return nil, err
	}
	for _, p := range resp.Pairs {
		if p["pair"] == name {
			return p, nil
		}
	}
	return nil, fmt.Errorf("pair %q not found", name)
}

// --- simple commands ---

var tradesCommand = &cli.Command{
	Name:  "trades",
	Usage: "list fulfilled trades, most recent first",
	Flags: []cli.Flag{
		&cli.UintFlag{Name: "limit", Usage: "maximum number of trades to return (default 100)"},
	},
	Action: func(c *cli.Context) error {
		path := "/v1/trades"
		if c.IsSet("limit") {
			path += "?limit=" + strconv.FormatUint(uint64(c.Uint("limit")), 10)
		}
		return doGet(c, path)
	},
}

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

// getJSON fetches path and decodes the JSON response into out.
func getJSON(c *cli.Context, path string, out any) error {
	resp, err := httpClient.Get(serverURL(c) + path)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	// nolint:errcheck
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("server error (%d): %s", resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

func printJSON(v any) error {
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(out))
	return nil
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
