package grpcservice

import (
	"context"
	"encoding/hex"

	bancov1 "github.com/arkade-os/solver/api-spec/protobuf/gen/go/solverd/v1"
	"github.com/arkade-os/solver/internal/core/application"
)

type preimageHandler struct {
	bancov1.UnimplementedPreimageServiceServer
	svc *application.PreimageService
}

func newPreimageHandler(svc *application.PreimageService) bancov1.PreimageServiceServer {
	return &preimageHandler{svc: svc}
}

func (h *preimageHandler) GetSolverPubKey(
	_ context.Context, _ *bancov1.GetSolverPubKeyRequest,
) (*bancov1.GetSolverPubKeyResponse, error) {
	return &bancov1.GetSolverPubKeyResponse{
		SolverPubKey:   hex.EncodeToString(h.svc.SolverPubKey().SerializeCompressed()),
		EmulatorPubKey: hex.EncodeToString(h.svc.EmulatorPubKey().SerializeCompressed()),
	}, nil
}
