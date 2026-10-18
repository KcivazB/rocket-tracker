package agent

import (
	"context"
	"errors"
	"rocket-tracker/internal/domain"
	"rocket-tracker/internal/tilt"
	"strings"
	"time"
)

// DeliveryJournal is implemented by the local scoped transactional store.
type DeliveryJournal interface {
	PendingDeliveries(context.Context, domain.Destination) ([]domain.Delivery, error)
	CompleteDelivery(context.Context, domain.Delivery, bool) error
}

// Destination is the last confirmed identity, not an inference from a token.
func (a *Agent) Destination() domain.Destination {
	a.mu.Lock()
	defer a.mu.Unlock()
	return domain.Destination{Server: strings.TrimRight(a.cfg.Server, "/"), ServerID: a.cfg.ServerID, AccountID: a.cfg.AccountID}
}

// uploadJournal runs under uploadMu. Every pass authenticates its destination
// before reading a single payload, including after same-URL token rotation.
func (a *Agent) uploadJournal(ctx context.Context) (int, error) {
	hctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	hb, err := a.heartbeat(hctx)
	cancel()
	if err != nil {
		return 0, err
	}
	d := domain.Destination{Server: strings.TrimRight(a.Client.ServerURL(), "/"), ServerID: hb.ServerID, AccountID: hb.User.ID}
	if !d.Bound() {
		return 0, errors.New("server does not provide a verified delivery identity; upgrade the server")
	}
	items, err := a.Journal.PendingDeliveries(ctx, d)
	if err != nil {
		return 0, err
	}
	sent := 0
	for _, v := range items {
		rctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		var alert *tilt.Alert
		if v.Match != nil {
			alert, err = a.Client.SendMatch(rctx, v.Key, v.Match)
		} else {
			err = a.Client.SendTraining(rctx, v.Key, v.Training)
		}
		cancel()
		var rejected *RejectedError
		if err != nil && !errors.As(err, &rejected) {
			return sent, err
		}
		if err = a.Journal.CompleteDelivery(ctx, v, rejected != nil); err != nil {
			return sent, err
		}
		if rejected == nil {
			sent++
			if alert != nil && a.OnAlert != nil {
				a.OnAlert(alert)
			}
		}
	}
	return sent, nil
}
