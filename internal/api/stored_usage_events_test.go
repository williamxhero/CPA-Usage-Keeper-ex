package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestStoredExportWriterBoundsCancellationAndWriteErrors(t *testing.T) {
	var out bytes.Buffer
	w := &storedExportWriter{ctx: context.Background(), out: &out, remaining: 3}
	if n, err := w.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatal(n, err)
	}
	if n, err := w.Write([]byte("d")); n != 0 || err == nil || out.String() != "abc" {
		t.Fatal(n, err, out.String())
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w = &storedExportWriter{ctx: ctx, out: &out, remaining: 99}
	if n, err := w.Write([]byte("x")); n != 0 || !errors.Is(err, context.Canceled) {
		t.Fatal(n, err)
	}
	w = &storedExportWriter{ctx: context.Background(), out: storedFailWriter{}, remaining: 99}
	if _, err := w.Write([]byte("x")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatal(err)
	}
	for _, format := range []string{"json", "csv"} {
		err := writeStoredEventsExport(&storedExportWriter{ctx: context.Background(), out: io.Discard, remaining: 10}, format, func(emit func(storedUsageEventPayload) error) error {
			return emit(storedUsageEventPayload{StoredCostUSD: 1.25, StoredCostAvailable: true})
		})
		if err == nil {
			t.Fatal(format, "accepted over-limit export")
		}
	}
}

type storedFailWriter struct{}

func (storedFailWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
