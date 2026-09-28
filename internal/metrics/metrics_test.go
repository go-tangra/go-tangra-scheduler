package metrics

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/go-tangra/go-tangra/v4/observe"
)

const tenant = "0190f7c2-6a3e-7c1a-9b2e-2f6f9d1b4c55"

func render(t *testing.T, fm *observe.Metrics) string {
	t.Helper()
	rec := httptest.NewRecorder()
	fm.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/metrics", nil))
	return rec.Body.String()
}

func TestInstrumentsRenderWithClosedLabels(t *testing.T) {
	fm, err := observe.NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(fm.Meter(Scope), func(context.Context) (map[string]int64, error) {
		return map[string]int64{"ipam": 1, "lcm": 2}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.Run("ipam:scan-network", "ipam", "succeeded", 1500*time.Millisecond)
	m.Run("ipam:scan-network", "ipam", "failed", time.Second)
	m.Retry("ipam:scan-network", "ipam")
	m.Skipped("lcm:check-expiring-certificates", SkipOverlap)
	m.Missed("lcm:check-expiring-certificates", 3)
	m.Missed("lcm:check-expiring-certificates", 0) // nothing
	body := render(t, fm)
	for _, want := range []string{
		`scheduler_runs_total{module="ipam",status="succeeded",type="ipam:scan-network"} 1`,
		`scheduler_runs_total{module="ipam",status="failed",type="ipam:scan-network"} 1`,
		`scheduler_retries_total{module="ipam",type="ipam:scan-network"} 1`,
		`scheduler_occurrences_skipped_total{reason="overlap",type="lcm:check-expiring-certificates"} 1`,
		`scheduler_occurrences_missed_total{type="lcm:check-expiring-certificates"} 3`,
		`scheduler_run_duration_seconds_count{module="ipam",type="ipam:scan-network"} 2`,
		`scheduler_types_registered{module="lcm"} 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("metrics lack %s\n%s", want, body)
		}
	}
	// SR-007: no tenant ids anywhere in the scrape.
	if strings.Contains(body, tenant) || strings.Contains(body, "tenant") {
		t.Fatal("tenant label in metrics")
	}
}

func TestTypeCounterErrorAndNil(t *testing.T) {
	fm, err := observe.NewMetrics()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New(fm.Meter(Scope), func(context.Context) (map[string]int64, error) { return nil, errors.New("db down") }); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(render(t, fm), "scheduler_types_registered{") {
		t.Fatal("failed lookup observed")
	}
	var m *Metrics
	m.Run("a:b", "a", "succeeded", time.Second)
	m.Retry("a:b", "a")
	m.Skipped("a:b", SkipOverlap)
	m.Missed("a:b", 1)
	if _, err := New(noop.NewMeterProvider().Meter("x"), nil); err != nil {
		t.Fatal(err)
	}
}

// failing meter: every instrument constructor fails in turn.
type failMeter struct {
	noop.Meter
	failAt, n int
}

func (f *failMeter) fail() bool { f.n++; return f.n == f.failAt }

func (f *failMeter) Int64Counter(name string, o ...metric.Int64CounterOption) (metric.Int64Counter, error) {
	if f.fail() {
		return nil, errors.New("boom")
	}
	return f.Meter.Int64Counter(name, o...)
}

func (f *failMeter) Float64Histogram(name string, o ...metric.Float64HistogramOption) (metric.Float64Histogram, error) {
	if f.fail() {
		return nil, errors.New("boom")
	}
	return f.Meter.Float64Histogram(name, o...)
}

func (f *failMeter) Int64ObservableUpDownCounter(name string, o ...metric.Int64ObservableUpDownCounterOption) (metric.Int64ObservableUpDownCounter, error) {
	if f.fail() {
		return nil, errors.New("boom")
	}
	return f.Meter.Int64ObservableUpDownCounter(name, o...)
}

func TestConstructorErrors(t *testing.T) {
	counter := func(context.Context) (map[string]int64, error) { return nil, nil }
	for i := 1; i <= 6; i++ {
		if _, err := New(&failMeter{failAt: i}, counter); err == nil {
			t.Errorf("failure %d not surfaced", i)
		}
	}
}
