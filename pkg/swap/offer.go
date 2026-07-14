package swap

import (
	"bytes"
	"math"

	"github.com/arkade-os/arkd/pkg/ark-lib/asset"
	"github.com/arkade-os/arkd/pkg/ark-lib/extension"
	"github.com/btcsuite/btcd/wire"

	"github.com/arkade-os/solver/pkg/swap/contract"
)

type Offer struct {
	contract.Offer
	FundingTxid   string
	DepositAsset  *asset.AssetId // nil means "BTC"
	DepositAmount uint64         // expressed in satoshis
}

// NewOffer parses the extension from the transaction, extracts a swap offer,
// locates the swap output, and determines the deposit asset. Returns nil if
// the tx has no extension, no swap offer, or no matching swap output.
func NewOffer(tx *wire.MsgTx) (*Offer, error) {
	ext, err := extension.NewExtensionFromTx(tx)
	if err != nil {
		return nil, nil
	}
	return NewOfferFromExtension(tx, ext)
}

// NewOfferFromExtension produces an *Offer from a transaction whose ark
// extension has already been parsed (e.g. by builder.ForExtension). Returns
// nil if no swap offer is present in the extension or no matching swap
// output is found.
func NewOfferFromExtension(tx *wire.MsgTx, ext extension.Extension) (*Offer, error) {
	offer, err := contract.FindSwapOffer(ext)
	if err != nil {
		return nil, err
	}
	if offer == nil {
		return nil, nil
	}

	var swapOutputValue int64
	var swapOutputIndex int
	for i, out := range tx.TxOut {
		if bytes.Equal(out.PkScript, offer.SwapPkScript) {
			swapOutputValue = out.Value
			swapOutputIndex = i
			break
		}
	}
	if swapOutputValue <= 0 {
		return nil, nil
	}

	o := &Offer{
		Offer:         *offer,
		FundingTxid:   tx.TxID(),
		DepositAmount: uint64(swapOutputValue),
	}

	for _, asst := range ext.GetAssetPacket() {
		for _, out := range asst.Outputs {
			if out.Vout == uint16(swapOutputIndex) {
				if asst.AssetId != nil {
					o.DepositAsset = asst.AssetId
					o.DepositAmount = out.Amount
				}
				break
			}
		}
	}

	return o, nil
}

// IsBTCDeposit returns true if the deposit side is native BTC.
func (o *Offer) IsBTCDeposit() bool {
	return o.DepositAsset == nil
}

// DepositAssetStr returns the deposit asset ID as a string, or "BTC".
func (o *Offer) DepositAssetStr() string {
	if o.DepositAsset == nil {
		return "BTC"
	}
	return o.DepositAsset.String()
}

// WantAssetStr returns the want asset ID as a string, or "BTC".
func (o *Offer) WantAssetStr() string {
	if o.WantAsset == nil {
		return "BTC"
	}
	return o.WantAsset.String()
}

// ComputePrice calculates the offer price as base/quote using the pair's
// decimal configuration. Returns 0, false if amounts are invalid.
func (o *Offer) ComputePrice(pair *Pair) (float64, bool) {
	if o.DepositAmount <= 0 || o.WantAmount <= 0 {
		return 0, false
	}
	baseAmount := float64(o.DepositAmount) / math.Pow10(pair.BaseDecimals)
	quoteAmount := float64(o.WantAmount) / math.Pow10(pair.QuoteDecimals)
	return baseAmount / quoteAmount, true
}
