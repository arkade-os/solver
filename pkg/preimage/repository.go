package preimage

import "context"

// ClaimCredentials holds everything needed to spend a preimage-gated VTXO.
// Keyed by PkScript in the registry.
type ClaimCredentials struct {
	Preimage     []byte
	ClaimAddress string
	PkScript     []byte
	ArkadeScript []byte
	Taptree      []string // hex-encoded closure scripts; output of TapscriptsVtxoScript.Encode()
}

// ClaimInfo is the secret-free view returned by ListClaims.
type ClaimInfo struct {
	ClaimAddress string
}

// Repository is the read-only port the plugin uses. The CRUD-extended port
// lives in internal/core/ports.
type Repository interface {
	Get(ctx context.Context, pkScript []byte) (ClaimCredentials, bool, error)
}
