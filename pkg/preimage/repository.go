package preimage

// ClaimCredentials is the secret material recovered from an Ark transaction
// and consumed by BuildClaim. The field set tracks exactly what BuildClaim
// reads — no more, no less.
type ClaimCredentials struct {
	Preimage     []byte
	ArkadeScript []byte
	Taptree      []string // hex-encoded closure scripts; output of TapscriptsVtxoScript.Encode()
	PkScript     []byte   // 34-byte P2TR pkScript of the claim VTXO
}
