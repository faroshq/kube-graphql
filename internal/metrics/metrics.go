package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	// QueryDuration tracks query execution latency.
	QueryDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: "kuery",
			Name:      "query_duration_seconds",
			Help:      "Histogram of query execution duration in seconds.",
			Buckets:   prometheus.ExponentialBuckets(0.001, 2, 15), // 1ms to ~16s
		},
		[]string{"has_relations", "incomplete"},
	)

	// QueryErrors counts query errors by type.
	QueryErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "kuery",
			Name:      "query_errors_total",
			Help:      "Total number of query errors by type.",
		},
		[]string{"error_type"},
	)

	// ObjectsTotal tracks the total number of synced objects per cluster.
	ObjectsTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kuery",
			Name:      "objects_total",
			Help:      "Total number of synced objects per cluster.",
		},
		[]string{"cluster"},
	)

	// ClustersTotal tracks cluster counts by status.
	ClustersTotal = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kuery",
			Name:      "clusters_total",
			Help:      "Total number of clusters by status.",
		},
		[]string{"status"},
	)

	// SyncLag tracks the lag between now and last_seen per cluster.
	SyncLag = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: "kuery",
			Name:      "sync_lag_seconds",
			Help:      "Seconds since last sync event per cluster.",
		},
		[]string{"cluster"},
	)
)

func init() {
	prometheus.MustRegister(
		QueryDuration,
		QueryErrors,
		ObjectsTotal,
		ClustersTotal,
		SyncLag,
	)
}
