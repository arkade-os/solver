package builder

import (
	"context"
	"errors"

	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/btcsuite/btcd/btcutil/psbt"

	"github.com/arkade-os/solver/pkg/solver/txmatch"
)

// ExtensionDecoder receives the parsed ark Extension and returns a typed
// intent. Return ErrSkip if the tx isn't for this plugin.
type ExtensionDecoder[T any] func(ctx context.Context, tx *psbt.Packet, ext extension.Extension) (T, error)

// TLVDecoder receives the matching TLV packet and returns a typed intent.
type TLVDecoder[T any] func(ctx context.Context, tx *psbt.Packet, pkt extension.Packet) (T, error)

// ForExtension returns a Builder pre-loaded with a cheap "tx has an ark
// extension" filter and a Decode stage that parses the OP_RETURN extension
// and hands the parsed Extension to the caller's decoder. Use this when the
// plugin needs multi-TLV access (e.g. main payload plus the asset packet).
func ForExtension[T any](decode ExtensionDecoder[T]) *Builder[T] {
	if decode == nil {
		panic("builder: ForExtension requires a decoder")
	}
	return For[T]().
		Filter(HasArkExtension()).
		Decode(func(ctx context.Context, tx *psbt.Packet) (T, error) {
			var zero T
			if tx == nil || tx.UnsignedTx == nil {
				return zero, ErrSkip
			}
			ext, err := extension.NewExtensionFromTx(tx.UnsignedTx)
			if err != nil {
				if errors.Is(err, extension.ErrExtensionNotFound) {
					return zero, ErrSkip
				}
				return zero, err
			}
			return decode(ctx, tx, ext)
		})
}

// HasArkExtension matches transactions whose outputs include at least one
// ark extension OP_RETURN script. The check is cheap — single script test
// per output, no full extension deserialize — so it's safe to run on every
// tx in the stream.
func HasArkExtension() txmatch.Predicate {
	return txmatch.HasOutput(txmatch.OutputScriptMatching(extension.IsExtension))
}
