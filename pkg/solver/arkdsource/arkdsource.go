// Package arkdsource provides an arkd-backed source of *psbt.Packet for the
// solver runtime. It is split from pkg/solver to keep the solver package
// free of any arkd/go-sdk dependency.
package arkdsource

import (
	"context"
	"strings"

	arksdk "github.com/arkade-os/go-sdk"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/sirupsen/logrus"
)

// Subscribe subscribes to arkd's transaction stream and returns a channel
// of *psbt.Packet for every ArkTx event. The channel is closed when:
//   - ctx is canceled
//   - the upstream stream errors out at subscribe time
//   - the upstream stream is closed
//
// Decoding errors on individual events are logged and skipped — the consumer
// receives only successfully-parsed packets.
func Subscribe(
	ctx context.Context,
	c arksdk.Wallet,
	log logrus.FieldLogger,
) <-chan *psbt.Packet {
	if log == nil {
		log = logrus.StandardLogger()
	}

	out := make(chan *psbt.Packet, 1)
	go func() {
		defer close(out)

		eventsCh, stop, err := c.Client().GetTransactionsStream(ctx)
		if err != nil {
			log.WithError(err).Error("failed to subscribe to arkd transaction stream")
			return
		}
		defer func() {
			if stop != nil {
				stop()
			}
		}()

		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-eventsCh:
				if !ok {
					log.Debug("arkd transaction stream closed")
					return
				}
				if ev.Err != nil {
					log.WithError(ev.Err).Warn("arkd stream error")
					continue
				}
				if ev.ArkTx == nil {
					continue
				}
				pkt, perr := psbt.NewFromRawBytes(strings.NewReader(ev.ArkTx.Tx), true)
				if perr != nil {
					log.WithError(perr).WithField("txid", ev.ArkTx.Txid).
						Warn("failed to decode arkd tx as psbt")
					continue
				}
				select {
				case out <- pkt:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return out
}
