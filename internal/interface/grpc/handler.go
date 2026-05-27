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

type handler struct {
	bancov1.UnimplementedBancoServiceServer

	svc *application.TakerService
}

func newHandler(svc *application.TakerService) bancov1.BancoServiceServer {
	return &handler{svc: svc}
}

func (h *handler) AddPair(
	ctx context.Context, req *bancov1.AddPairRequest,
) (*bancov1.AddPairResponse, error) {
	if req.Pair == nil {
		return nil, status.Error(codes.InvalidArgument, "pair is required")
	}

	pair := protoToDomain(req.Pair)
	if err := h.svc.AddPair(ctx, pair); err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s", err)
	}
	return &bancov1.AddPairResponse{}, nil
}

func (h *handler) UpdatePair(
	ctx context.Context, req *bancov1.UpdatePairRequest,
) (*bancov1.UpdatePairResponse, error) {
	if req.Pair == nil {
		return nil, status.Error(codes.InvalidArgument, "pair is required")
	}

	pair := protoToDomain(req.Pair)
	if err := h.svc.UpdatePair(ctx, pair); err != nil {
		if errors.Is(err, ports.ErrPairNotFound) {
			return nil, status.Errorf(codes.NotFound, "%s", err)
		}
		return nil, status.Errorf(codes.InvalidArgument, "%s", err)
	}
	return &bancov1.UpdatePairResponse{}, nil
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
	status := h.svc.Status()
	return &bancov1.GetStatusResponse{Running: status.Running}, nil
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
		Pair:        p.Pair,
		MinAmount:   p.MinAmount,
		MaxAmount:   p.MaxAmount,
		PriceFeed:   p.PriceFeed,
		InvertPrice: p.InvertPrice,
	}
}

func domainToProto(p banco.Pair) *bancov1.PairInfo {
	return &bancov1.PairInfo{
		Pair:        p.Pair,
		MinAmount:   p.MinAmount,
		MaxAmount:   p.MaxAmount,
		PriceFeed:   p.PriceFeed,
		InvertPrice: p.InvertPrice,
	}
}
