package grpcservice

import (
	"context"
	"errors"
	"strconv"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	swapv1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
	"github.com/arkade-os/solver/internal/core/application"
	"github.com/arkade-os/solver/internal/core/ports"
	"github.com/arkade-os/solver/pkg/swap"
)

type SwapService interface {
	AddMarket(ctx context.Context, m swap.Market) (swap.Market, error)
	UpdateMarket(ctx context.Context, m swap.Market) (swap.Market, error)
	RemoveMarket(ctx context.Context, base, quote string) error
	ListMarkets(ctx context.Context) ([]swap.Market, error)
	ListTrades(ctx context.Context, limit int, status string) ([]ports.Trade, error)
	GetBalance(ctx context.Context) (*application.Balance, error)
	GetAddress(ctx context.Context) (*application.Address, error)
	ListAssets(ctx context.Context) ([]application.AssetInfo, error)
	RegistryCard(ctx context.Context, name string) (*application.Card, error)
	SendOffchain(ctx context.Context, password, address, assetID string, amount uint64) (string, error)
	CollaborativeExit(ctx context.Context, password, address string, amount uint64) (string, error)
	Settle(ctx context.Context, password string) (string, error)
	GetConfig() application.OperatorConfig
	DumpSeed(ctx context.Context, password string) (string, error)
}

type handler struct {
	swapv1.UnimplementedSwapServiceServer

	svc SwapService
}

func newHandler(svc SwapService) *handler {
	return &handler{svc: svc}
}

func (h *handler) AddMarket(
	ctx context.Context, req *swapv1.AddMarketRequest,
) (*swapv1.AddMarketResponse, error) {
	if req.Market == nil {
		return nil, status.Error(codes.InvalidArgument, "market is required")
	}

	stored, err := h.svc.AddMarket(ctx, protoToDomain(req.Market))
	if err != nil {
		if errors.Is(err, ports.ErrMarketExists) {
			return nil, status.Errorf(codes.AlreadyExists, "%s", err)
		}
		return nil, status.Errorf(codes.InvalidArgument, "%s", err)
	}
	return &swapv1.AddMarketResponse{Market: domainToProto(stored)}, nil
}

func (h *handler) UpdateMarket(
	ctx context.Context, req *swapv1.UpdateMarketRequest,
) (*swapv1.UpdateMarketResponse, error) {
	if req.Market == nil {
		return nil, status.Error(codes.InvalidArgument, "market is required")
	}

	stored, err := h.svc.UpdateMarket(ctx, protoToDomain(req.Market))
	if err != nil {
		if errors.Is(err, ports.ErrMarketNotFound) {
			return nil, status.Errorf(codes.NotFound, "%s", err)
		}
		return nil, status.Errorf(codes.InvalidArgument, "%s", err)
	}
	return &swapv1.UpdateMarketResponse{Market: domainToProto(stored)}, nil
}

func (h *handler) RemoveMarket(
	ctx context.Context, req *swapv1.RemoveMarketRequest,
) (*swapv1.RemoveMarketResponse, error) {
	if err := h.svc.RemoveMarket(ctx, req.BaseAsset, req.QuoteAsset); err != nil {
		return nil, status.Errorf(codes.Internal, "%s", err)
	}
	return &swapv1.RemoveMarketResponse{}, nil
}

func (h *handler) ListMarkets(
	ctx context.Context, _ *swapv1.ListMarketsRequest,
) (*swapv1.ListMarketsResponse, error) {
	markets, err := h.svc.ListMarkets(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list markets: %s", err)
	}

	protoMarkets := make([]*swapv1.MarketInfo, 0, len(markets))
	for _, m := range markets {
		protoMarkets = append(protoMarkets, domainToProto(m))
	}

	return &swapv1.ListMarketsResponse{Markets: protoMarkets}, nil
}

func (h *handler) GetStatus(
	ctx context.Context, _ *swapv1.GetStatusRequest,
) (*swapv1.GetStatusResponse, error) {
	return &swapv1.GetStatusResponse{Running: true}, nil
}

func (h *handler) GetBalance(
	ctx context.Context, _ *swapv1.GetBalanceRequest,
) (*swapv1.GetBalanceResponse, error) {
	bal, err := h.svc.GetBalance(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get balance: %s", err)
	}
	return &swapv1.GetBalanceResponse{
		OnchainConfirmed:   bal.OnchainSpendable,
		OnchainUnconfirmed: bal.OnchainLocked,
		OffchainSettled:    bal.OffchainTotal,
		AssetBalances:      bal.AssetBalances,
	}, nil
}

func (h *handler) GetAddress(
	ctx context.Context, _ *swapv1.GetAddressRequest,
) (*swapv1.GetAddressResponse, error) {
	addr, err := h.svc.GetAddress(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get address: %s", err)
	}
	return &swapv1.GetAddressResponse{
		OffchainAddress: addr.OffchainAddress,
		BoardingAddress: addr.BoardingAddress,
	}, nil
}

func (h *handler) ListAssets(
	ctx context.Context, _ *swapv1.ListAssetsRequest,
) (*swapv1.ListAssetsResponse, error) {
	assets, err := h.svc.ListAssets(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list assets: %s", err)
	}
	out := make([]*swapv1.AssetInfo, 0, len(assets))
	for _, a := range assets {
		out = append(out, &swapv1.AssetInfo{
			AssetId:  a.AssetID,
			Ticker:   a.Ticker,
			Name:     a.Name,
			IconUrl:  a.IconURL,
			Decimals: a.Decimals,
			Balance:  a.Balance,
		})
	}
	return &swapv1.ListAssetsResponse{Assets: out}, nil
}

func (h *handler) GetRegistryCard(
	ctx context.Context, req *swapv1.GetRegistryCardRequest,
) (*swapv1.RegistryCard, error) {
	card, err := h.svc.RegistryCard(ctx, req.GetName())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s", err)
	}
	return cardToProto(card), nil
}

func cardToProto(c *application.Card) *swapv1.RegistryCard {
	markets := make([]*swapv1.RegistryMarket, 0, len(c.Markets))
	for _, m := range c.Markets {
		markets = append(markets, &swapv1.RegistryMarket{
			Pair:            m.Pair,
			BaseAsset:       cardAssetToProto(m.BaseAsset),
			QuoteAsset:      cardAssetToProto(m.QuoteAsset),
			PriceFeed:       m.PriceFeed,
			PriceFeedSchema: &swapv1.RegistryPriceFeedSchema{Type: "json", PricePath: m.PricePath},
			PriceDecimals:   int32(m.PriceDecimals), //nolint:gosec
			FeeBps:          m.FeeBps,
			MinBaseAmount:   strconv.FormatUint(m.MinBaseAmount, 10),
			MaxBaseAmount:   strconv.FormatUint(m.MaxBaseAmount, 10),
			MinQuoteAmount:  strconv.FormatUint(m.MinQuoteAmount, 10),
			MaxQuoteAmount:  strconv.FormatUint(m.MaxQuoteAmount, 10),
		})
	}
	return &swapv1.RegistryCard{Version: c.Version, Name: c.Name, Markets: markets}
}

func cardAssetToProto(a application.CardAsset) *swapv1.RegistryAsset {
	return &swapv1.RegistryAsset{
		Id:       a.ID,
		Name:     a.Name,
		Ticker:   a.Ticker,
		Decimals: int32(a.Decimals), //nolint:gosec
	}
}

func (h *handler) SendOffchain(
	ctx context.Context, req *swapv1.SendOffchainRequest,
) (*swapv1.SendOffchainResponse, error) {
	txid, err := h.svc.SendOffchain(ctx, req.GetPassword(), req.GetAddress(), req.GetAssetId(), req.GetAmount())
	if err != nil {
		return nil, walletOpError(err)
	}
	return &swapv1.SendOffchainResponse{Txid: txid}, nil
}

func (h *handler) CollaborativeExit(
	ctx context.Context, req *swapv1.CollaborativeExitRequest,
) (*swapv1.CollaborativeExitResponse, error) {
	txid, err := h.svc.CollaborativeExit(ctx, req.GetPassword(), req.GetAddress(), req.GetAmount())
	if err != nil {
		return nil, walletOpError(err)
	}
	return &swapv1.CollaborativeExitResponse{Txid: txid}, nil
}

func (h *handler) Settle(
	ctx context.Context, req *swapv1.SettleRequest,
) (*swapv1.SettleResponse, error) {
	txid, err := h.svc.Settle(ctx, req.GetPassword())
	if err != nil {
		return nil, walletOpError(err)
	}
	return &swapv1.SettleResponse{Txid: txid}, nil
}

func (h *handler) GetConfig() application.OperatorConfig {
	return h.svc.GetConfig()
}

func (h *handler) DumpSeed(ctx context.Context, password string) (string, error) {
	seed, err := h.svc.DumpSeed(ctx, password)
	if err != nil {
		return "", walletOpError(err)
	}
	return seed, nil
}

// walletOpError maps wallet operation errors to gRPC status codes: a bad
// password is Unauthenticated, everything else is treated as an invalid
// argument so the operator sees the underlying message.
func walletOpError(err error) error {
	if errors.Is(err, application.ErrInvalidPassword) {
		return status.Error(codes.Unauthenticated, err.Error())
	}
	return status.Errorf(codes.InvalidArgument, "%s", err)
}

func (h *handler) ListTrades(
	ctx context.Context, req *swapv1.ListTradesRequest,
) (*swapv1.ListTradesResponse, error) {
	trades, err := h.svc.ListTrades(ctx, int(req.GetLimit()), req.GetStatus())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list trades: %s", err)
	}
	out := make([]*swapv1.TradeInfo, 0, len(trades))
	for _, t := range trades {
		out = append(out, &swapv1.TradeInfo{
			Id:            t.ID,
			Market:        t.Market,
			DepositAsset:  t.DepositAsset,
			DepositAmount: t.DepositAmount,
			WantAsset:     t.WantAsset,
			WantAmount:    t.WantAmount,
			OfferTxid:     t.OfferTxid,
			FulfillTxid:   t.FulfillTxid,
			Error:         t.Error,
			CreatedAt:     t.CreatedAt.Unix(),
		})
	}
	return &swapv1.ListTradesResponse{Trades: out}, nil
}

func protoToDomain(m *swapv1.MarketInfo) swap.Market {
	return swap.Market{
		BaseAsset:       m.BaseAsset,
		QuoteAsset:      m.QuoteAsset,
		BaseDecimals:    int(m.BaseDecimals),
		QuoteDecimals:   int(m.QuoteDecimals),
		MinQuoteAmount:  m.MinQuoteAmount,
		MaxQuoteAmount:  m.MaxQuoteAmount,
		MinBaseAmount:   m.MinBaseAmount,
		MaxBaseAmount:   m.MaxBaseAmount,
		PriceFeed:       m.PriceFeed,
		PricePath:       m.PricePath,
		SlippageBps:     m.SlippageBps,
		FeeBps:          m.FeeBps,
		PriceTTLSeconds: m.PriceTtlSeconds,
	}
}

func domainToProto(m swap.Market) *swapv1.MarketInfo {
	return &swapv1.MarketInfo{
		BaseAsset:       m.BaseAsset,
		QuoteAsset:      m.QuoteAsset,
		BaseDecimals:    int32(m.BaseDecimals),  //nolint:gosec
		QuoteDecimals:   int32(m.QuoteDecimals), //nolint:gosec
		MinQuoteAmount:  m.MinQuoteAmount,
		MaxQuoteAmount:  m.MaxQuoteAmount,
		MinBaseAmount:   m.MinBaseAmount,
		MaxBaseAmount:   m.MaxBaseAmount,
		PriceFeed:       m.PriceFeed,
		PricePath:       m.PricePath,
		SlippageBps:     m.SlippageBps,
		FeeBps:          m.FeeBps,
		PriceTtlSeconds: m.PriceTTLSeconds,
	}
}
