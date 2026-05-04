package bounty

import (
	"regexp"
	"strconv"

	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/wire"
)

// arkd surfaces per-input rejections via gRPC errors whose message contains:
//
//	VTXO_ALREADY_SPENT (6): <txid>:<vout> already spent
//	VTXO_RECOVERABLE (8): <txid>:<vout> is recoverable
//
// Both indicate "this specific input cannot participate in the claim right
// now"; the rest of the batch can still proceed. The error string itself is
// wrapped by the introspector and by us, so we extract by regex rather than
// typed errors.
var spentOutpointRE = regexp.MustCompile(`VTXO_(?:ALREADY_SPENT|RECOVERABLE)[^:]*:\s*([0-9a-fA-F]{64}):(\d+)`)

// parseSpentOutpoints scans an error message for per-input arkd rejections
// (already-spent or recoverable) and returns the set of offending outpoints.
// Returns nil if the error is not such a partial rejection (timeout, network
// error, signature error, …) — callers treat that case as "unknown error,
// give up retrying".
func parseSpentOutpoints(err error) map[wire.OutPoint]bool {
	if err == nil {
		return nil
	}
	matches := spentOutpointRE.FindAllStringSubmatch(err.Error(), -1)
	if len(matches) == 0 {
		return nil
	}
	out := make(map[wire.OutPoint]bool, len(matches))
	for _, m := range matches {
		hash, hashErr := chainhash.NewHashFromStr(m[1])
		if hashErr != nil {
			continue
		}
		vout, voutErr := strconv.ParseUint(m[2], 10, 32)
		if voutErr != nil {
			continue
		}
		out[wire.OutPoint{Hash: *hash, Index: uint32(vout)}] = true
	}
	return out
}
