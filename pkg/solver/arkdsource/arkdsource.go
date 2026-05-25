// Package arkdsource provides an arkd-backed solver.Source. It is split
// from pkg/solver to keep the solver package free of any arkd/go-sdk
// dependency.
package arkdsource

import (
	"context"
	"strings"

	"github.com/arkade-os/arkd/pkg/client-lib/client"
	"github.com/btcsuite/btcd/btcutil/psbt"
	"github.com/sirupsen/logrus"
)

// Source subscribes to arkd's transaction stream and exposes it as a
// solver.Source. Each call to Subscribe opens a fresh upstream stream so
// per-plugin server-side filtering stays isolated.
type Source struct {
	c   client.Client
	log logrus.FieldLogger
}

// New returns a Source backed by the given arkd client.
func New(c client.Client, log logrus.FieldLogger) *Source {
	if log == nil {
		log = logrus.StandardLogger()
	}
	return &Source{c: c, log: log}
}

// Subscribe opens an arkd transaction stream and forwards each ArkTx event
// as a decoded *psbt.Packet. The filter parameter is accepted for forward
// compatibility — arkd's CEL filter support is not yet wired through, so
// the upstream subscription is currently unfiltered.
//
// The returned channel is closed when:
//   - ctx is canceled
//   - the upstream stream errors out at subscribe time
//   - the upstream stream is closed
//
// Decoding errors on individual events are logged and skipped — the
// consumer receives only successfully-parsed packets.
func (s *Source) Subscribe(ctx context.Context, _ string) (<-chan *psbt.Packet, error) {
	eventsCh, stop, err := s.c.GetTransactionsStream(ctx)
	if err != nil {
		return nil, err
	}

	out := make(chan *psbt.Packet, 1)
	go func() {
		defer close(out)
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
					s.log.Debug("arkd transaction stream closed")
					return
				}
				if ev.Err != nil {
					s.log.WithError(ev.Err).Warn("arkd stream error")
					continue
				}
				if ev.ArkTx == nil {
					continue
				}
				pkt, perr := psbt.NewFromRawBytes(strings.NewReader(ev.ArkTx.Tx), true)
				if perr != nil {
					s.log.WithError(perr).WithField("txid", ev.ArkTx.Txid).
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
	return out, nil
}
