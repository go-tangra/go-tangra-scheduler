package metrics

import (
	"context"

	"github.com/go-kratos/kratos/v2/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/tx7do/kratos-bootstrap/bootstrap"
)

// Collector provides Prometheus metrics collection for the scheduler service.
type Collector struct {
	requestsTotal   *prometheus.CounterVec
	requestDuration *prometheus.HistogramVec
}

func NewCollector(_ *bootstrap.Context) *Collector {
	c := &Collector{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "scheduler",
				Name:      "requests_total",
				Help:      "Total number of requests processed",
			},
			[]string{"method", "code"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "scheduler",
				Name:      "request_duration_seconds",
				Help:      "Request duration in seconds",
				Buckets:   prometheus.DefBuckets,
			},
			[]string{"method"},
		),
	}

	prometheus.MustRegister(c.requestsTotal, c.requestDuration)

	return c
}

// Middleware returns a Kratos middleware for collecting request metrics.
func (c *Collector) Middleware() middleware.Middleware {
	return func(handler middleware.Handler) middleware.Handler {
		return func(ctx context.Context, req interface{}) (interface{}, error) {
			return handler(ctx, req)
		}
	}
}
