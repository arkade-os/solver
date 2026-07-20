package swap

import (
	"context"
	"time"
)

// FulfillmentAttempt describes an attempt to fulfill a swap offer. Error is
// empty when the offer was fulfilled, and otherwise holds the reason it was
// rejected or the fulfillment failed; FulfillTxid is set only on success.
// Amounts are in raw base units (sats for BTC, or the asset's own base unit).
type FulfillmentAttempt struct {
	Market        string
	DepositAsset  string
	DepositAmount uint64
	WantAsset     string
	WantAmount    uint64
	OfferTxid     string
	FulfillTxid   string
	Error         string
	Timestamp     time.Time
}

// AttemptListener is notified when the swap plugin finishes an attempt to
// fulfill an offer it matched to a market, whether or not it succeeded.
// Implementations should return quickly; any persistence or network I/O should
// be non-blocking or delegated to a background goroutine.
type AttemptListener interface {
	OnAttempt(ctx context.Context, evt FulfillmentAttempt)
}
