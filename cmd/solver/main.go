// Command solver is the CLI for the solverd operator: manage markets, wallet and trades.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/mdp/qrterminal/v3"
	"github.com/urfave/cli/v2"
	"golang.org/x/term"
)

var (
	Version    string
	httpClient = &http.Client{Timeout: 5 * time.Minute} // settlement may take time
	useColor   bool
	unicodeOK  bool
)

func main() {
	app := &cli.App{
		Name:    "solver",
		Usage:   "solverd operator CLI — manage markets, wallet and trades",
		Version: Version,
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "server",
				Usage:   "solverd HTTP server address",
				Value:   "http://localhost:7171",
				EnvVars: []string{"SOLVER_SERVER"},
			},
			&cli.BoolFlag{
				Name:    "json",
				Aliases: []string{"j"},
				Usage:   "print raw JSON instead of formatted output (for scripting)",
			},
		},
		Before: func(*cli.Context) error {
			useColor = term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("NO_COLOR") == ""
			unicodeOK = localeIsUTF8()
			return nil
		},
		Commands: []*cli.Command{
			marketCommand,
			balanceCommand,
			addressCommand,
			sendCommand,
			exitCommand,
			settleCommand,
			tradesCommand,
			statusCommand,
		},
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintln(os.Stderr, red("error: ")+err.Error())
		os.Exit(1)
	}
}

var marketCommand = &cli.Command{
	Name:    "market",
	Aliases: []string{"markets"},
	Usage:   "manage trading markets",
	Subcommands: []*cli.Command{
		{
			Name:   "add",
			Usage:  "add a new market",
			Flags:  marketFlags(true),
			Action: marketAdd,
		},
		{
			Name:   "update",
			Usage:  "update a market (only the given flags change)",
			Flags:  marketFlags(false),
			Action: marketUpdate,
		},
		{
			Name:  "get",
			Usage: "show a single market",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "base", Required: true, Usage: "base asset id or BTC"},
				&cli.StringFlag{Name: "quote", Required: true, Usage: "quote asset id or BTC"},
			},
			Action: marketGet,
		},
		{
			Name:  "remove",
			Usage: "remove a market",
			Flags: []cli.Flag{
				&cli.StringFlag{Name: "base", Required: true, Usage: "base asset id or BTC"},
				&cli.StringFlag{Name: "quote", Required: true, Usage: "quote asset id or BTC"},
			},
			Action: marketRemove,
		},
		{
			Name:    "list",
			Aliases: []string{"ls"},
			Usage:   "list all markets",
			Action:  marketList,
		},
	},
}

func marketAdd(c *cli.Context) error {
	base, quote := c.String("base"), c.String("quote")
	return mutate(c, http.MethodPost, "/v1/market",
		map[string]any{"market": marketOverrides(c)}, "Market "+base+"/"+quote+" added")
}

func marketUpdate(c *cli.Context) error {
	base, quote := c.String("base"), c.String("quote")
	current, err := fetchMarket(c, base, quote)
	if err != nil {
		return err
	}
	maps.Copy(current, marketOverrides(c))
	return mutate(c, http.MethodPut, "/v1/market",
		map[string]any{"market": current}, "Market "+base+"/"+quote+" updated")
}

func marketGet(c *cli.Context) error {
	markets, err := listMarkets(c)
	if err != nil {
		return err
	}
	base, quote := c.String("base"), c.String("quote")
	for _, m := range markets {
		if m.BaseAsset == base && m.QuoteAsset == quote {
			if c.Bool("json") {
				out, _ := json.MarshalIndent(m, "", "  ") //nolint:errcheck
				fmt.Println(string(out))
				return nil
			}
			renderMarketDetail(m, loadAssets(c))
			return nil
		}
	}
	return fmt.Errorf("market %s/%s not found", base, quote)
}

func marketRemove(c *cli.Context) error {
	base, quote := c.String("base"), c.String("quote")
	return mutate(c, http.MethodDelete,
		fmt.Sprintf("/v1/market/%s/%s", url.PathEscape(base), url.PathEscape(quote)), nil,
		"Market "+base+"/"+quote+" removed")
}

func marketList(c *cli.Context) error {
	data, err := request(c, http.MethodGet, "/v1/markets", nil)
	if err != nil {
		return err
	}
	if c.Bool("json") {
		return printRaw(data)
	}
	var resp struct {
		Markets []marketInfo `json:"markets"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return err
	}
	renderMarkets(resp.Markets, loadAssets(c))
	return nil
}

// marketFlags: base/quote always identify the market; for add, price-feed is
// also required. min/max bounds are always optional (0 = disabled).
func marketFlags(required bool) []cli.Flag {
	return []cli.Flag{
		&cli.StringFlag{Name: "base", Required: true, Usage: "base asset id or BTC"},
		&cli.StringFlag{Name: "quote", Required: true, Usage: "quote asset id or BTC"},
		&cli.Uint64Flag{Name: "min-quote", Usage: "min want amount for sell-base (quote units); 0 = disabled"},
		&cli.Uint64Flag{Name: "max-quote", Usage: "max want amount for sell-base (quote units); 0 = disabled"},
		&cli.Uint64Flag{Name: "min-base", Usage: "min want amount for buy-base (base units); 0 = disabled"},
		&cli.Uint64Flag{Name: "max-base", Usage: "max want amount for buy-base (base units); 0 = disabled"},
		&cli.StringFlag{Name: "price-feed", Required: required, Usage: "price feed URL (quote-per-base)"},
		&cli.Uint64Flag{Name: "slippage", Usage: "max deviation in bps (0 = server default)"},
		&cli.Uint64Flag{Name: "fee", Usage: "solver margin in bps, shifted into the price (0 = none)"},
	}
}

func marketOverrides(c *cli.Context) map[string]any {
	out := map[string]any{"base_asset": c.String("base"), "quote_asset": c.String("quote")}
	if c.IsSet("min-quote") {
		out["min_quote_amount"] = c.Uint64("min-quote")
	}
	if c.IsSet("max-quote") {
		out["max_quote_amount"] = c.Uint64("max-quote")
	}
	if c.IsSet("min-base") {
		out["min_base_amount"] = c.Uint64("min-base")
	}
	if c.IsSet("max-base") {
		out["max_base_amount"] = c.Uint64("max-base")
	}
	if c.IsSet("price-feed") {
		out["price_feed"] = c.String("price-feed")
	}
	if c.IsSet("slippage") {
		out["slippage_bps"] = c.Uint64("slippage")
	}
	if c.IsSet("fee") {
		out["fee_bps"] = c.Uint64("fee")
	}
	return out
}

// fetchMarket returns the market as raw fields so update can preserve unset ones.
func fetchMarket(c *cli.Context, base, quote string) (map[string]any, error) {
	var resp struct {
		Markets []map[string]any `json:"markets"`
	}
	if err := getJSON(c, "/v1/markets", &resp); err != nil {
		return nil, err
	}
	for _, m := range resp.Markets {
		if m["base_asset"] == base && m["quote_asset"] == quote {
			return m, nil
		}
	}
	return nil, fmt.Errorf("market %s/%s not found", base, quote)
}

func listMarkets(c *cli.Context) ([]marketInfo, error) {
	var resp struct {
		Markets []marketInfo `json:"markets"`
	}
	if err := getJSON(c, "/v1/markets", &resp); err != nil {
		return nil, err
	}
	return resp.Markets, nil
}

var balanceCommand = &cli.Command{
	Name:    "balance",
	Aliases: []string{"bal"},
	Usage:   "show the wallet balance",
	Action: func(c *cli.Context) error {
		data, err := request(c, http.MethodGet, "/v1/balance", nil)
		if err != nil {
			return err
		}
		if c.Bool("json") {
			return printRaw(data)
		}
		var b balanceResp
		if err := json.Unmarshal(data, &b); err != nil {
			return err
		}
		renderBalance(b, loadAssets(c))
		return nil
	},
}

var addressCommand = &cli.Command{
	Name:  "address",
	Usage: "show wallet receive addresses",
	Action: func(c *cli.Context) error {
		data, err := request(c, http.MethodGet, "/v1/address", nil)
		if err != nil {
			return err
		}
		if c.Bool("json") {
			return printRaw(data)
		}
		var a struct {
			OffchainAddress string `json:"offchain_address"`
			BoardingAddress string `json:"boarding_address"`
		}
		if err := json.Unmarshal(data, &a); err != nil {
			return err
		}
		renderAddress(a.OffchainAddress, a.BoardingAddress)
		return nil
	},
}

var sendCommand = &cli.Command{
	Name:  "send",
	Usage: "send funds to an Ark (offchain) address",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "to", Required: true, Usage: "destination Ark address (ark1…/tark1…)"},
		&cli.Uint64Flag{Name: "amount", Required: true, Usage: "amount — sats for BTC, base units for assets"},
		&cli.StringFlag{Name: "asset", Value: "BTC", Usage: "asset to send: BTC or a hex asset id"},
		passwordFlag(),
	},
	Action: func(c *cli.Context) error {
		password, err := readPassword(c)
		if err != nil {
			return err
		}
		return txResult(c, "/v1/wallet/send", map[string]any{
			"password": password,
			"address":  c.String("to"),
			"asset_id": c.String("asset"),
			"amount":   c.Uint64("amount"),
		}, "Sent")
	},
}

var exitCommand = &cli.Command{
	Name:  "exit",
	Usage: "collaboratively exit BTC to an onchain address",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "to", Required: true, Usage: "destination onchain address (bc1…/bcrt1…)"},
		&cli.Uint64Flag{Name: "amount", Required: true, Usage: "amount in sats"},
		passwordFlag(),
	},
	Action: func(c *cli.Context) error {
		password, err := readPassword(c)
		if err != nil {
			return err
		}
		return txResult(c, "/v1/wallet/exit", map[string]any{
			"password": password,
			"address":  c.String("to"),
			"amount":   c.Uint64("amount"),
		}, "Exit submitted")
	},
}

var settleCommand = &cli.Command{
	Name:  "settle",
	Usage: "settle pending and boarding funds by joining a batch round",
	Flags: []cli.Flag{passwordFlag()},
	Action: func(c *cli.Context) error {
		password, err := readPassword(c)
		if err != nil {
			return err
		}
		return txResult(c, "/v1/wallet/settle", map[string]any{"password": password}, "Settled")
	},
}

func passwordFlag() cli.Flag {
	return &cli.StringFlag{
		Name:    "password",
		Usage:   "wallet password (prompts if omitted, or set SOLVER_PASSWORD)",
		EnvVars: []string{"SOLVER_PASSWORD"},
	}
}

func readPassword(c *cli.Context) (string, error) {
	if p := c.String("password"); p != "" {
		return p, nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", errors.New("wallet password required: pass --password or set SOLVER_PASSWORD")
	}
	fmt.Fprint(os.Stderr, "Wallet password: ")
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	if len(b) == 0 {
		return "", errors.New("empty password")
	}
	return string(b), nil
}

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
		data, err := request(c, http.MethodGet, path, nil)
		if err != nil {
			return err
		}
		if c.Bool("json") {
			return printRaw(data)
		}
		var resp struct {
			Trades []tradeInfo `json:"trades"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return err
		}
		renderTrades(resp.Trades, loadAssets(c))
		return nil
	},
}

var statusCommand = &cli.Command{
	Name:  "status",
	Usage: "show whether the solver is running",
	Action: func(c *cli.Context) error {
		data, err := request(c, http.MethodGet, "/v1/status", nil)
		if err != nil {
			return err
		}
		if c.Bool("json") {
			return printRaw(data)
		}
		var s struct {
			Running bool `json:"running"`
		}
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		if s.Running {
			fmt.Println(green(gBullet() + " solverd is running"))
		} else {
			fmt.Println(red(gCircle() + " solverd is not running"))
		}
		return nil
	},
}

type marketInfo struct {
	BaseAsset      string `json:"base_asset"`
	QuoteAsset     string `json:"quote_asset"`
	BaseDecimals   int32  `json:"base_decimals"`
	QuoteDecimals  int32  `json:"quote_decimals"`
	MinQuoteAmount uint64 `json:"min_quote_amount"`
	MaxQuoteAmount uint64 `json:"max_quote_amount"`
	MinBaseAmount  uint64 `json:"min_base_amount"`
	MaxBaseAmount  uint64 `json:"max_base_amount"`
	PriceFeed      string `json:"price_feed"`
	SlippageBps    uint32 `json:"slippage_bps"`
	FeeBps         uint32 `json:"fee_bps"`
}

type assetInfo struct {
	AssetId  string `json:"asset_id"`
	Ticker   string `json:"ticker"`
	Name     string `json:"name"`
	Decimals int32  `json:"decimals"`
}

type tradeInfo struct {
	Market        string `json:"market"`
	DepositAsset  string `json:"deposit_asset"`
	DepositAmount uint64 `json:"deposit_amount"`
	WantAsset     string `json:"want_asset"`
	WantAmount    uint64 `json:"want_amount"`
	OfferTxid     string `json:"offer_txid"`
	FulfillTxid   string `json:"fulfill_txid"`
	CreatedAt     int64  `json:"created_at"`
}

type balanceResp struct {
	OnchainConfirmed uint64            `json:"onchain_confirmed"`
	OffchainSettled  uint64            `json:"offchain_settled"`
	OffchainPending  uint64            `json:"offchain_pending"`
	AssetBalances    map[string]uint64 `json:"asset_balances"`
}

func loadAssets(c *cli.Context) map[string]assetInfo {
	out := map[string]assetInfo{}
	data, err := request(c, http.MethodGet, "/v1/assets", nil)
	if err != nil {
		return out
	}
	var resp struct {
		Assets []assetInfo `json:"assets"`
	}
	if json.Unmarshal(data, &resp) == nil {
		for _, a := range resp.Assets {
			out[a.AssetId] = a
		}
	}
	return out
}

func renderMarkets(markets []marketInfo, meta map[string]assetInfo) {
	fmt.Println(bold(fmt.Sprintf("Markets (%d)", len(markets))))
	if len(markets) == 0 {
		fmt.Println(dim("  none yet — add one with: solver market add --base BTC --quote <asset> --price-feed .."))
		return
	}
	fmt.Println()
	tw := newTable()
	fmt.Fprintln(tw, "  MARKET\tMIN QUOTE\tMAX QUOTE\tMIN BASE\tMAX BASE\tTOLERANCE\tFEE\tFEED") //nolint:errcheck
	for _, m := range markets {
		market := unitLabel(m.BaseAsset, meta) + " " + gArrowBoth() + " " + unitLabel(m.QuoteAsset, meta)
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
			market,
			fmtBound(m.MinQuoteAmount, m.QuoteAsset, m.QuoteDecimals, meta),
			fmtBound(m.MaxQuoteAmount, m.QuoteAsset, m.QuoteDecimals, meta),
			fmtBound(m.MinBaseAmount, m.BaseAsset, m.BaseDecimals, meta),
			fmtBound(m.MaxBaseAmount, m.BaseAsset, m.BaseDecimals, meta),
			fmtTolerance(m.SlippageBps), fmtBps(m.FeeBps), feedHost(m.PriceFeed))
	}
	tw.Flush() //nolint:errcheck
}

func renderMarketDetail(m marketInfo, meta map[string]assetInfo) {
	fmt.Println(bold("Market " + unitLabel(m.BaseAsset, meta) + " " + gArrowBoth() + " " + unitLabel(m.QuoteAsset, meta)))
	fmt.Println()
	tw := newTable()
	row := func(k, v string) { fmt.Fprintf(tw, "  %s\t%s\n", dim(k), v) } //nolint:errcheck
	row("Base", unitLabel(m.BaseAsset, meta))
	row("Quote", unitLabel(m.QuoteAsset, meta))
	row("Min want (sell base)", fmtBound(m.MinQuoteAmount, m.QuoteAsset, m.QuoteDecimals, meta))
	row("Max want (sell base)", fmtBound(m.MaxQuoteAmount, m.QuoteAsset, m.QuoteDecimals, meta))
	row("Min want (buy base)", fmtBound(m.MinBaseAmount, m.BaseAsset, m.BaseDecimals, meta))
	row("Max want (buy base)", fmtBound(m.MaxBaseAmount, m.BaseAsset, m.BaseDecimals, meta))
	row("Tolerance", fmtTolerance(m.SlippageBps))
	row("Fee", fmtBps(m.FeeBps))
	row("Price feed", m.PriceFeed)
	tw.Flush() //nolint:errcheck
}

func renderBalance(b balanceResp, meta map[string]assetInfo) {
	fmt.Println(bold("Balance"))
	fmt.Println()
	tw := newTable()
	fmt.Fprintln(tw, "  ASSET\tAVAILABLE\t") //nolint:errcheck

	var detail []string
	if b.OnchainConfirmed > 0 {
		detail = append(detail, "onchain "+fmtScaled(b.OnchainConfirmed, 8))
	}
	if b.OffchainPending > 0 {
		detail = append(detail, "pending "+fmtScaled(b.OffchainPending, 8))
	}
	fmt.Fprintf(tw, "  %s\t%s\t%s\n", "BTC", fmtScaled(b.OffchainSettled, 8)+" BTC", //nolint:errcheck
		dim(strings.Join(detail, " "+glyph("·", "|")+" ")))

	for _, id := range sortedKeys(b.AssetBalances) {
		amt := b.AssetBalances[id]
		if amt == 0 {
			continue
		}
		fmt.Fprintf(tw, "  %s\t%s\t\n", unitLabel(id, meta), fmtValue(amt, id, meta)) //nolint:errcheck
	}
	tw.Flush() //nolint:errcheck
}

func renderTrades(trades []tradeInfo, meta map[string]assetInfo) {
	fmt.Println(bold(fmt.Sprintf("Trades (%d)", len(trades))))
	if len(trades) == 0 {
		fmt.Println(dim("  none yet"))
		return
	}
	fmt.Println()
	tw := newTable()
	fmt.Fprintln(tw, "  WHEN\tTRADE\tDEPOSITED\tFILLED\tOFFER\tFILL") //nolint:errcheck
	for _, t := range trades {
		trade := unitLabel(t.DepositAsset, meta) + " " + gArrow() + " " + unitLabel(t.WantAsset, meta)
		fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\t%s\n", //nolint:errcheck
			relTime(t.CreatedAt), trade,
			fmtValue(t.DepositAmount, t.DepositAsset, meta),
			fmtValue(t.WantAmount, t.WantAsset, meta),
			shortTxid(t.OfferTxid), shortTxid(t.FulfillTxid))
	}
	tw.Flush() //nolint:errcheck
}

func renderAddress(offchain, boarding string) {
	fmt.Println(bold("Addresses"))
	fmt.Println()
	tw := newTable()
	fmt.Fprintf(tw, "  %s\t%s\n", dim("Offchain (Ark)"), offchain)     //nolint:errcheck
	fmt.Fprintf(tw, "  %s\t%s\n", dim("Boarding (onchain)"), boarding) //nolint:errcheck
	tw.Flush()                                                         //nolint:errcheck
	if useColor && unicodeOK && offchain != "" {
		fmt.Println()
		fmt.Println(dim("  Scan to send to the offchain address:"))
		qrterminal.GenerateHalfBlock(offchain, qrterminal.L, os.Stdout)
	}
}

func serverURL(c *cli.Context) string {
	return c.String("server")
}

func request(c *cli.Context, method, path string, body any) ([]byte, error) {
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, serverURL(c)+path, r)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cannot reach solverd at %s (%w)", serverURL(c), err)
	}
	defer resp.Body.Close() //nolint:errcheck
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, serverError(resp.StatusCode, data)
	}
	return data, nil
}

func serverError(code int, data []byte) error {
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &e) //nolint:errcheck
	switch {
	case e.Error != "":
		return errors.New(e.Error)
	case e.Message != "":
		return errors.New(e.Message)
	default:
		return fmt.Errorf("server error (%d): %s", code, strings.TrimSpace(string(data)))
	}
}

func getJSON(c *cli.Context, path string, out any) error {
	data, err := request(c, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func mutate(c *cli.Context, method, path string, body any, success string) error {
	data, err := request(c, method, path, body)
	if err != nil {
		return err
	}
	if c.Bool("json") {
		return printRaw(data)
	}
	fmt.Println(green(gCheck()+" ") + success)
	return nil
}

func txResult(c *cli.Context, path string, body any, verb string) error {
	data, err := request(c, http.MethodPost, path, body)
	if err != nil {
		return err
	}
	if c.Bool("json") {
		return printRaw(data)
	}
	var r struct {
		Txid string `json:"txid"`
	}
	_ = json.Unmarshal(data, &r) //nolint:errcheck
	fmt.Println(green(gCheck()+" ") + verb + "  " + dim("txid ") + r.Txid)
	return nil
}

func printRaw(data []byte) error {
	var buf bytes.Buffer
	if json.Indent(&buf, data, "", "  ") == nil {
		fmt.Println(buf.String())
	} else {
		fmt.Println(strings.TrimSpace(string(data)))
	}
	return nil
}

func newTable() *tabwriter.Writer {
	return tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
}

func unitLabel(id string, meta map[string]assetInfo) string {
	if id == "BTC" || id == "" {
		return "BTC"
	}
	if m, ok := meta[id]; ok {
		if m.Ticker != "" {
			return m.Ticker
		}
		if m.Name != "" {
			return m.Name
		}
	}
	return truncID(id)
}

func truncID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:6] + gEllipsis() + id[len(id)-4:]
}

// fmtBound shows a market's want-side bound: BTC as sats, an asset scaled + ticker.
func fmtBound(raw uint64, id string, dec int32, meta map[string]assetInfo) string {
	if id == "BTC" || id == "" {
		return group(strconv.FormatUint(raw, 10)) + " sats"
	}
	if dec == 0 {
		if m, ok := meta[id]; ok {
			dec = m.Decimals
		}
	}
	return fmtScaled(raw, dec) + " " + unitLabel(id, meta)
}

// fmtValue shows a trade amount: BTC in BTC, an asset scaled + ticker.
func fmtValue(raw uint64, id string, meta map[string]assetInfo) string {
	if id == "BTC" || id == "" {
		return fmtScaled(raw, 8) + " BTC"
	}
	var dec int32
	if m, ok := meta[id]; ok {
		dec = m.Decimals
	}
	return fmtScaled(raw, dec) + " " + unitLabel(id, meta)
}

func fmtScaled(raw uint64, decimals int32) string {
	s := strconv.FormatUint(raw, 10)
	d := int(decimals)
	if d <= 0 {
		return group(s)
	}
	if len(s) <= d {
		s = strings.Repeat("0", d-len(s)+1) + s
	}
	return group(s[:len(s)-d]) + "." + s[len(s)-d:]
}

func group(s string) string {
	n := len(s)
	if n <= 3 {
		return s
	}
	var b strings.Builder
	pre := n % 3
	if pre > 0 {
		b.WriteString(s[:pre])
		b.WriteByte(',')
	}
	for i := pre; i < n; i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < n {
			b.WriteByte(',')
		}
	}
	return b.String()
}

// fmtBps renders basis points as a percentage; 0 reads as "none".
func fmtBps(bps uint32) string {
	if bps == 0 {
		return "none"
	}
	return strconv.FormatFloat(float64(bps)/100, 'f', -1, 64) + "%"
}

func fmtTolerance(bps uint32) string {
	if bps == 0 {
		bps = 10 // server default (DefaultSlippageBps)
	}
	return glyph("±", "+/-") + strconv.FormatFloat(float64(bps)/100, 'f', -1, 64) + "%"
}

func feedHost(raw string) string {
	if u, err := url.Parse(raw); err == nil && u.Host != "" {
		return u.Host
	}
	return raw
}

func relTime(unix int64) string {
	if unix == 0 {
		return gDash()
	}
	t := time.Unix(unix, 0)
	d := time.Since(t)
	switch {
	case d >= 0 && d < time.Minute:
		return "just now"
	case d >= 0 && d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d >= 0 && d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Format("2006-01-02 15:04")
	}
}

func shortTxid(id string) string {
	if id == "" {
		return dim(gDash())
	}
	if len(id) <= 14 {
		return id
	}
	return id[:6] + gEllipsis() + id[len(id)-6:]
}

func sortedKeys(m map[string]uint64) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}

func paint(code, s string) string {
	if !useColor {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func bold(s string) string  { return paint("1", s) }
func dim(s string) string   { return paint("2", s) }
func green(s string) string { return paint("32", s) }
func red(s string) string   { return paint("31", s) }

func localeIsUTF8() bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := os.Getenv(k); v != "" {
			v = strings.ToUpper(v)
			return strings.Contains(v, "UTF-8") || strings.Contains(v, "UTF8")
		}
	}
	return false
}

func glyph(unicode, ascii string) string {
	if unicodeOK {
		return unicode
	}
	return ascii
}

func gArrow() string     { return glyph("→", "->") }
func gArrowBoth() string { return glyph("⇄", "<>") }
func gEllipsis() string  { return glyph("…", "..") }
func gDash() string      { return glyph("—", "-") }
func gCheck() string     { return glyph("✓", "OK") }
func gBullet() string    { return glyph("●", "*") }
func gCircle() string    { return glyph("○", "-") }
