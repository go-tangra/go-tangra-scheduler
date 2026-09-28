// Package metrics holds the scheduler's OpenTelemetry instruments (research
// D8, US6). They are created on the framework's meter (freya
// App.Metrics().Meter), so the admin listener's /metrics renders them next to
// the framework instruments:
//
//	scheduler_runs_total{type,module,status}          finished attempts
//	scheduler_run_duration_seconds{type,module}        attempt duration
//	scheduler_retries_total{type,module}               retries scheduled
//	scheduler_occurrences_skipped_total{type,reason}   overlap | type_unavailable | payload_invalid | invalid_schedule
//	scheduler_occurrences_missed_total{type}           occurrences missed during downtime
//	scheduler_types_registered{module}                 available task types per module (observable)
//
// Labels carry registered identifiers and closed vocabularies only — never
// tenant ids, task ids or payload values (SR-007, SC-008). Every method is
// nil-safe so services can run without metrics in tests.
package metrics

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Scope is the instrumentation scope name.
const Scope = "github.com/go-tangra/go-tangra-scheduler/v4"

// Skip reasons (closed set).
const (
	SkipOverlap         = "overlap"
	SkipTypeUnavailable = "type_unavailable"
	SkipPayloadInvalid  = "payload_invalid"
	SkipInvalidSchedule = "invalid_schedule"
)

// TypeCounter reports the available task types per module (observable up-down counter: the framework renders sums and histograms).
type TypeCounter func(ctx context.Context) (map[string]int64, error)

// Metrics are the scheduler instruments.
type Metrics struct {
	runs     metric.Int64Counter
	duration metric.Float64Histogram
	retries  metric.Int64Counter
	skipped  metric.Int64Counter
	missed   metric.Int64Counter
}

// New creates the instruments on meter; types (optional) feeds the
// registered-types gauge.
func New(meter metric.Meter, types TypeCounter) (*Metrics, error) {
	m := &Metrics{}
	var err error
	if m.runs, err = meter.Int64Counter("scheduler.runs", metric.WithDescription("Finished task attempts by type, module and status")); err != nil {
		return nil, err
	}
	if m.duration, err = meter.Float64Histogram("scheduler.run.duration", metric.WithUnit("s"),
		metric.WithDescription("Task attempt duration")); err != nil {
		return nil, err
	}
	if m.retries, err = meter.Int64Counter("scheduler.retries", metric.WithDescription("Retries scheduled after a retryable failure")); err != nil {
		return nil, err
	}
	if m.skipped, err = meter.Int64Counter("scheduler.occurrences.skipped", metric.WithDescription("Occurrences skipped by reason")); err != nil {
		return nil, err
	}
	if m.missed, err = meter.Int64Counter("scheduler.occurrences.missed", metric.WithDescription("Occurrences missed while the scheduler was down")); err != nil {
		return nil, err
	}
	if types != nil {
		_, err = meter.Int64ObservableUpDownCounter("scheduler.types.registered", metric.WithDescription("Available task types per module"),
			metric.WithInt64Callback(func(ctx context.Context, o metric.Int64Observer) error {
				ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
				defer cancel()
				counts, err := types(ctx)
				if err != nil {
					return nil // a failed lookup reports nothing this cycle
				}
				for module, n := range counts {
					o.Observe(n, metric.WithAttributes(attribute.String("module", module)))
				}
				return nil
			}))
		if err != nil {
			return nil, err
		}
	}
	return m, nil
}

func typeAttrs(typeName, module string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String("type", typeName), attribute.String("module", module))
}

// Run records one finished attempt.
func (m *Metrics) Run(typeName, module, status string, d time.Duration) {
	if m == nil {
		return
	}
	ctx := context.Background()
	m.runs.Add(ctx, 1, metric.WithAttributes(attribute.String("type", typeName), attribute.String("module", module), attribute.String("status", status)))
	m.duration.Record(ctx, d.Seconds(), typeAttrs(typeName, module))
}

// Retry counts one scheduled retry.
func (m *Metrics) Retry(typeName, module string) {
	if m != nil {
		m.retries.Add(context.Background(), 1, typeAttrs(typeName, module))
	}
}

// Skipped counts one skipped occurrence.
func (m *Metrics) Skipped(typeName, reason string) {
	if m != nil {
		m.skipped.Add(context.Background(), 1, metric.WithAttributes(attribute.String("type", typeName), attribute.String("reason", reason)))
	}
}

// Missed counts n missed occurrences.
func (m *Metrics) Missed(typeName string, n int64) {
	if m != nil && n > 0 {
		m.missed.Add(context.Background(), n, metric.WithAttributes(attribute.String("type", typeName)))
	}
}
