package grpcservice

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	swapv1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
	"github.com/arkade-os/solver/internal/core/application"
	"github.com/arkade-os/solver/internal/core/ports"
	"github.com/arkade-os/solver/pkg/swap"
)

type SwapService interface {
	AddPair(ctx context.Context, pair swap.Pair) (swap.Pair, error)
	UpdatePair(ctx context.Context, pair swap.Pair) (swap.Pair, error)
	RemovePair(ctx context.Context, pairName string) error
	ListPairs(ctx context.Context) ([]swap.Pair, error)
	ListTrades(ctx context.Context, limit int) ([]ports.Trade, error)
	GetBalance(ctx context.Context) (*application.Balance, error)
	GetAddress(ctx context.Context) (*application.Address, error)
	ListAssets(ctx context.Context) ([]application.AssetInfo, error)
	SendOffchain(ctx context.Context, password, address, assetID string, amount uint64) (string, error)
	CollaborativeExit(ctx context.Context, password, address string, amount uint64) (string, error)
	Settle(ctx context.Context, password string) (string, error)
}

type handler struct {
	swapv1.UnimplementedSwapServiceServer

	svc SwapService
}

func newHandler(svc SwapService) *handler {
	return &handler{svc: svc}
}

func (h *handler) AddPair(
	ctx context.Context, req *swapv1.AddPairRequest,
) (*swapv1.AddPairResponse, error) {
	if req.Pair == nil {
		return nil, status.Error(codes.InvalidArgument, "pair is required")
	}

	pair := protoToDomain(req.Pair)
	stored, err := h.svc.AddPair(ctx, pair)
	if err != nil {
		if errors.Is(err, ports.ErrPairExists) {
			return nil, status.Errorf(codes.AlreadyExists, "%s", err)
		}
		return nil, status.Errorf(codes.InvalidArgument, "%s", err)
	}
	return &swapv1.AddPairResponse{Pair: domainToProto(stored)}, nil
}

func (h *handler) UpdatePair(
	ctx context.Context, req *swapv1.UpdatePairRequest,
) (*swapv1.UpdatePairResponse, error) {
	if req.Pair == nil {
		return nil, status.Error(codes.InvalidArgument, "pair is required")
	}

	pair := protoToDomain(req.Pair)
	stored, err := h.svc.UpdatePair(ctx, pair)
	if err != nil {
		if errors.Is(err, ports.ErrPairNotFound) {
			return nil, status.Errorf(codes.NotFound, "%s", err)
		}
		return nil, status.Errorf(codes.InvalidArgument, "%s", err)
	}
	return &swapv1.UpdatePairResponse{Pair: domainToProto(stored)}, nil
}

func (h *handler) RemovePair(
	ctx context.Context, req *swapv1.RemovePairRequest,
) (*swapv1.RemovePairResponse, error) {
	if err := h.svc.RemovePair(ctx, req.Pair); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s", err)
	}
	return &swapv1.RemovePairResponse{}, nil
}

func (h *handler) ListPairs(
	ctx context.Context, _ *swapv1.ListPairsRequest,
) (*swapv1.ListPairsResponse, error) {
	pairs, err := h.svc.ListPairs(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list pairs: %s", err)
	}

	protoPairs := make([]*swapv1.PairInfo, 0, len(pairs))
	for _, p := range pairs {
		protoPairs = append(protoPairs, domainToProto(p))
	}

	return &swapv1.ListPairsResponse{Pairs: protoPairs}, nil
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
	trades, err := h.svc.ListTrades(ctx, int(req.GetLimit()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list trades: %s", err)
	}
	out := make([]*swapv1.TradeInfo, 0, len(trades))
	for _, t := range trades {
		out = append(out, &swapv1.TradeInfo{
			Id:            t.ID,
			Pair:          t.Pair,
			DepositAsset:  t.DepositAsset,
			DepositAmount: t.DepositAmount,
			WantAsset:     t.WantAsset,
			WantAmount:    t.WantAmount,
			OfferTxid:     t.OfferTxid,
			FulfillTxid:   t.FulfillTxid,
			CreatedAt:     t.CreatedAt.Unix(),
		})
	}
	return &swapv1.ListTradesResponse{Trades: out}, nil
}

func protoToDomain(p *swapv1.PairInfo) swap.Pair {
	return swap.Pair{
		Pair:        p.Pair,
		MinAmount:   p.MinAmount,
		MaxAmount:   p.MaxAmount,
		PriceFeed:   p.PriceFeed,
		InvertPrice: p.InvertPrice,
		SlippageBps: p.SlippageBps,
	}
}

func domainToProto(p swap.Pair) *swapv1.PairInfo {
	return &swapv1.PairInfo{
		Pair:          p.Pair,
		MinAmount:     p.MinAmount,
		MaxAmount:     p.MaxAmount,
		PriceFeed:     p.PriceFeed,
		InvertPrice:   p.InvertPrice,
		SlippageBps:   p.SlippageBps,
		BaseDecimals:  int32(p.BaseDecimals),  //nolint:gosec
		QuoteDecimals: int32(p.QuoteDecimals), //nolint:gosec
	}
}
