// Package application owns recording and ingestion use cases through narrow ports.
package application

import (
	"context"
	"errors"
	"fmt"
	"rocket-tracker/internal/domain"
	"strings"
)

var ErrInvalidMatch = errors.New("invalid match")

type ResultIngester interface {
	Ingest(context.Context, *domain.Match, string) error
}
type TrainingIngester interface {
	AddTraining(context.Context, *domain.Training, string) error
}

func validTag(t string) bool {
	switch strings.ToLower(t) {
	case "ranked", "casual", "tournament", "private", "other":
		return true
	}
	return false
}

func IngestResult(ctx context.Context, sc ResultIngester, defaultTag, key string, m *domain.Match) error {
	switch {
	case m == nil:
		return fmt.Errorf("%w: missing match", ErrInvalidMatch)
	case len(key) > 200:
		return fmt.Errorf("%w: key too long", ErrInvalidMatch)
	case m.Result != "win" && m.Result != "loss" && m.Result != "abandoned":
		return fmt.Errorf("%w: result must be win, loss or abandoned", ErrInvalidMatch)
	case m.StartedAt.IsZero() || m.EndedAt.Before(m.StartedAt):
		return fmt.Errorf("%w: invalid match times", ErrInvalidMatch)
	}
	if err := ValidateMatchQuantities(m); err != nil {
		return err
	}
	m.ID = 0
	m.Tag = strings.ToLower(strings.TrimSpace(m.Tag))
	if !validTag(m.Tag) {
		m.Tag = defaultTag
	}
	return sc.Ingest(ctx, m, key)
}

func IngestTraining(ctx context.Context, sc TrainingIngester, key string, t *domain.Training) error {
	if t == nil {
		return fmt.Errorf("%w: missing training", domain.ErrInvalidTraining)
	}
	if len(key) > 200 {
		return fmt.Errorf("%w: key too long", domain.ErrInvalidTraining)
	}
	if err := t.Normalize(); err != nil {
		return err
	}
	t.ID = 0
	return sc.AddTraining(ctx, t, key)
}
