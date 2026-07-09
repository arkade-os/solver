package grpcservice

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	bancov1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
	"github.com/arkade-os/solver/internal/core/application"
	"github.com/arkade-os/solver/internal/core/ports"
	"github.com/arkade-os/solver/pkg/banco"
)

type BancoService interface {
	AddPair(ctx context.Context, pair banco.Pair) (banco.Pair, error)
	UpdatePair(ctx context.Context, pair banco.Pair) (banco.Pair, error)
	RemovePair(ctx context.Context, pairName string) error
	ListPairs(ctx context.Context) ([]banco.Pair, error)
	ListTrades(ctx context.Context, limit int) ([]ports.Trade, error)
	GetBalance(ctx context.Context) (*application.Balance, error)
	GetAddress(ctx context.Context) (*application.Address, error)
	ListAssets(ctx context.Context) ([]application.AssetInfo, error)
	SendOffchain(ctx context.Context, password, address, assetID string, amount uint64) (string, error)
	CollaborativeExit(ctx context.Context, password, address string, amount uint64) (string, error)
	Settle(ctx context.Context, password string) (string, error)
}

type handler struct {
	bancov1.UnimplementedBancoServiceServer

	svc BancoService
}

func newHandler(svc BancoService) *handler {
	return &handler{svc: svc}
}

func (h *handler) AddPair(
	ctx context.Context, req *bancov1.AddPairRequest,
) (*bancov1.AddPairResponse, error) {
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
	return &bancov1.AddPairResponse{Pair: domainToProto(stored)}, nil
}

func (h *handler) UpdatePair(
	ctx context.Context, req *bancov1.UpdatePairRequest,
) (*bancov1.UpdatePairResponse, error) {
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
	return &bancov1.UpdatePairResponse{Pair: domainToProto(stored)}, nil
}

func (h *handler) RemovePair(
	ctx context.Context, req *bancov1.RemovePairRequest,
) (*bancov1.RemovePairResponse, error) {
	if err := h.svc.RemovePair(ctx, req.Pair); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s", err)
	}
	return &bancov1.RemovePairResponse{}, nil
}

func (h *handler) ListPairs(
	ctx context.Context, _ *bancov1.ListPairsRequest,
) (*bancov1.ListPairsResponse, error) {
	pairs, err := h.svc.ListPairs(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list pairs: %s", err)
	}

	protoPairs := make([]*bancov1.PairInfo, 0, len(pairs))
	for _, p := range pairs {
		protoPairs = append(protoPairs, domainToProto(p))
	}

	return &bancov1.ListPairsResponse{Pairs: protoPairs}, nil
}

func (h *handler) GetStatus(
	ctx context.Context, _ *bancov1.GetStatusRequest,
) (*bancov1.GetStatusResponse, error) {
	return &bancov1.GetStatusResponse{Running: true}, nil
}

func (h *handler) GetBalance(
	ctx context.Context, _ *bancov1.GetBalanceRequest,
) (*bancov1.GetBalanceResponse, error) {
	bal, err := h.svc.GetBalance(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get balance: %s", err)
	}
	return &bancov1.GetBalanceResponse{
		OnchainConfirmed:   bal.OnchainSpendable,
		OnchainUnconfirmed: bal.OnchainLocked,
		OffchainSettled:    bal.OffchainTotal,
		AssetBalances:      bal.AssetBalances,
	}, nil
}

func (h *handler) GetAddress(
	ctx context.Context, _ *bancov1.GetAddressRequest,
) (*bancov1.GetAddressResponse, error) {
	addr, err := h.svc.GetAddress(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get address: %s", err)
	}
	return &bancov1.GetAddressResponse{
		OffchainAddress: addr.OffchainAddress,
		BoardingAddress: addr.BoardingAddress,
	}, nil
}

func (h *handler) ListAssets(
	ctx context.Context, _ *bancov1.ListAssetsRequest,
) (*bancov1.ListAssetsResponse, error) {
	assets, err := h.svc.ListAssets(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list assets: %s", err)
	}
	out := make([]*bancov1.AssetInfo, 0, len(assets))
	for _, a := range assets {
		out = append(out, &bancov1.AssetInfo{
			AssetId:  a.AssetID,
			Ticker:   a.Ticker,
			Name:     a.Name,
			IconUrl:  a.IconURL,
			Decimals: a.Decimals,
			Balance:  a.Balance,
		})
	}
	return &bancov1.ListAssetsResponse{Assets: out}, nil
}

func (h *handler) SendOffchain(
	ctx context.Context, req *bancov1.SendOffchainRequest,
) (*bancov1.SendOffchainResponse, error) {
	txid, err := h.svc.SendOffchain(ctx, req.GetPassword(), req.GetAddress(), req.GetAssetId(), req.GetAmount())
	if err != nil {
		return nil, walletOpError(err)
	}
	return &bancov1.SendOffchainResponse{Txid: txid}, nil
}

func (h *handler) CollaborativeExit(
	ctx context.Context, req *bancov1.CollaborativeExitRequest,
) (*bancov1.CollaborativeExitResponse, error) {
	txid, err := h.svc.CollaborativeExit(ctx, req.GetPassword(), req.GetAddress(), req.GetAmount())
	if err != nil {
		return nil, walletOpError(err)
	}
	return &bancov1.CollaborativeExitResponse{Txid: txid}, nil
}

func (h *handler) Settle(
	ctx context.Context, req *bancov1.SettleRequest,
) (*bancov1.SettleResponse, error) {
	txid, err := h.svc.Settle(ctx, req.GetPassword())
	if err != nil {
		return nil, walletOpError(err)
	}
	return &bancov1.SettleResponse{Txid: txid}, nil
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
	ctx context.Context, req *bancov1.ListTradesRequest,
) (*bancov1.ListTradesResponse, error) {
	trades, err := h.svc.ListTrades(ctx, int(req.GetLimit()))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list trades: %s", err)
	}
	out := make([]*bancov1.TradeInfo, 0, len(trades))
	for _, t := range trades {
		out = append(out, &bancov1.TradeInfo{
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
	return &bancov1.ListTradesResponse{Trades: out}, nil
}

func protoToDomain(p *bancov1.PairInfo) banco.Pair {
	return banco.Pair{
		Pair:          p.Pair,
		MinBaseAmount: p.MinBaseAmount,
		MaxBaseAmount: p.MaxBaseAmount,
		PriceFeed:     p.PriceFeed,
		InvertPrice:   p.InvertPrice,
		ToleranceBps:  p.ToleranceBps,
		FeeBps:        p.FeeBps,
	}
}

func domainToProto(p banco.Pair) *bancov1.PairInfo {
	return &bancov1.PairInfo{
		Pair:          p.Pair,
		MinBaseAmount: p.MinBaseAmount,
		MaxBaseAmount: p.MaxBaseAmount,
		PriceFeed:     p.PriceFeed,
		InvertPrice:   p.InvertPrice,
		ToleranceBps:  p.ToleranceBps,
		FeeBps:        p.FeeBps,
		BaseDecimals:  int32(p.BaseDecimals),  //nolint:gosec
		QuoteDecimals: int32(p.QuoteDecimals), //nolint:gosec
	}
}
