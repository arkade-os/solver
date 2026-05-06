package grpcservice

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	bancov1 "github.com/arkade-os/bancod/api-spec/protobuf/gen/go/bancod/v1"
	"github.com/arkade-os/bancod/internal/core/application"
	"github.com/arkade-os/bancod/internal/core/ports"
)

type preimageHandler struct {
	bancov1.UnimplementedPreimageServiceServer
	svc *application.PreimageService
}

func newPreimageHandler(svc *application.PreimageService) bancov1.PreimageServiceServer {
	return &preimageHandler{svc: svc}
}

func (h *preimageHandler) RegisterClaim(
	ctx context.Context, req *bancov1.RegisterClaimRequest,
) (*bancov1.RegisterClaimResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is nil")
	}
	claimAddress, err := h.svc.RegisterClaim(ctx, application.RegisterClaimInput{
		Preimage:     req.Preimage,
		ArkadeScript: req.ArkadeScript,
		Taptree:      req.Taptree,
	})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "%s", err)
	}
	return &bancov1.RegisterClaimResponse{ClaimAddress: claimAddress}, nil
}

func (h *preimageHandler) ListClaims(
	ctx context.Context, _ *bancov1.ListClaimsRequest,
) (*bancov1.ListClaimsResponse, error) {
	infos, err := h.svc.ListClaims(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list claims: %s", err)
	}
	out := make([]*bancov1.ClaimInfo, 0, len(infos))
	for _, ci := range infos {
		out = append(out, &bancov1.ClaimInfo{ClaimAddress: ci.ClaimAddress})
	}
	return &bancov1.ListClaimsResponse{Claims: out}, nil
}

func (h *preimageHandler) DeleteClaim(
	ctx context.Context, req *bancov1.DeleteClaimRequest,
) (*bancov1.DeleteClaimResponse, error) {
	if req == nil || req.ClaimAddress == "" {
		return nil, status.Error(codes.InvalidArgument, "claim_address is required")
	}
	if err := h.svc.DeleteClaim(ctx, req.ClaimAddress); err != nil {
		if errors.Is(err, ports.ErrClaimNotFound) {
			return nil, status.Errorf(codes.NotFound, "%s", err)
		}
		return nil, status.Errorf(codes.InvalidArgument, "%s", err)
	}
	return &bancov1.DeleteClaimResponse{}, nil
}
