package application

import (
	"context"
	"errors"
	"math"
	"rocket-tracker/internal/domain"
	"testing"
)

type fakeRecords struct {
	calls       int
	fail        error
	destination *domain.Destination
}

func (f *fakeRecords) RecordResult(_ context.Context, _ *domain.Match, d *domain.Destination) error {
	f.calls++
	f.destination = d
	return f.fail
}
func (f *fakeRecords) RecordTraining(_ context.Context, _ *domain.Training, d *domain.Destination) error {
	f.calls++
	f.destination = d
	return f.fail
}
func TestRecorderOnlyNotifiesCommittedRecords(t *testing.T) {
	ctx := context.Background()
	f := new(fakeRecords)
	notifications := 0
	d := &domain.Destination{Server: "https://test.invalid"}
	r := Recorder{Records: f, Destination: func() (*domain.Destination, error) { return d, nil }, AfterResult: func(*domain.Match) { notifications++ }, AfterTraining: func(*domain.Training) { notifications++ }}
	f.fail = errors.New("transaction failed")
	if e := r.RecordResult(ctx, &domain.Match{}); e == nil || notifications != 0 {
		t.Fatal("notified failed commit")
	}
	if e := r.RecordTraining(ctx, &domain.Training{}); e == nil || notifications != 0 {
		t.Fatal("notified failed training")
	}
	f.fail = nil
	if e := r.RecordResult(ctx, &domain.Match{}); e != nil {
		t.Fatal(e)
	}
	if e := r.RecordTraining(ctx, &domain.Training{}); e != nil {
		t.Fatal(e)
	}
	if notifications != 2 || f.destination != d {
		t.Fatal("commit/destination contract")
	}
	before := f.calls
	r.Destination = func() (*domain.Destination, error) { return nil, errors.New("unreadable configuration") }
	if e := r.RecordResult(ctx, &domain.Match{}); e == nil || f.calls != before {
		t.Fatal("configuration error silently became local only")
	}
}

type fakeIngester struct{ calls int }

func (f *fakeIngester) Ingest(context.Context, *domain.Match, string) error { f.calls++; return nil }
func TestIngestRejectsBeforeCallingPort(t *testing.T) {
	f := new(fakeIngester)
	if e := IngestResult(context.Background(), f, "ranked", "", &domain.Match{Result: "draw"}); !errors.Is(e, ErrInvalidMatch) || f.calls != 0 {
		t.Fatal("invalid result reached persistence", e)
	}
}

func TestNewMeasurementFieldsValidated(t *testing.T) {
	for _, sample := range []float64{-1, math.NaN(), math.Inf(1)} {
		m := &domain.Match{Movement: &domain.Movement{CarSampleS: &sample}}
		if err := ValidateMatchQuantities(m); !errors.Is(err, ErrInvalidMatch) {
			t.Fatalf("sample %v accepted", sample)
		}
	}
	if err := ValidateMatchQuantities(&domain.Match{SpeedUnit: "guessed"}); !errors.Is(err, ErrInvalidMatch) {
		t.Fatal("unrecognized unit accepted")
	}
	for _, unit := range []domain.SpeedUnit{"", domain.SpeedUnknown, domain.SpeedUUPerSecond, domain.SpeedKilometresPerHour} {
		if err := ValidateMatchQuantities(&domain.Match{SpeedUnit: unit}); err != nil {
			t.Fatal(unit, err)
		}
	}
}
