package sls

import (
	"sync"
	"time"
)

// SLSMetrics holds statistics about SLS log shipping.
type SLSMetrics struct {
	TotalLogs     int64
	SuccessLogs   int64
	FailedLogs    int64
	LastError     error
	LastErrorTime time.Time
	mu            sync.RWMutex
}

func (m *SLSMetrics) IncrementTotal() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TotalLogs++
}

func (m *SLSMetrics) IncrementSuccess() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.SuccessLogs++
}

func (m *SLSMetrics) IncrementFailed(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.FailedLogs++
	m.LastError = err
	m.LastErrorTime = time.Now().UTC()
}

// GetMetrics returns a copy of the current metrics.
func (m *SLSMetrics) GetMetrics() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()

	metrics := map[string]interface{}{
		"total_logs":   m.TotalLogs,
		"success_logs": m.SuccessLogs,
		"failed_logs":  m.FailedLogs,
	}
	if m.LastError != nil {
		metrics["last_error"] = m.LastError.Error()
		metrics["last_error_time"] = m.LastErrorTime.Format(time.RFC3339)
	}
	if m.TotalLogs > 0 {
		metrics["error_rate"] = float64(m.FailedLogs) / float64(m.TotalLogs)
	}
	return metrics
}

func (m *SLSMetrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.TotalLogs = 0
	m.SuccessLogs = 0
	m.FailedLogs = 0
	m.LastError = nil
	m.LastErrorTime = time.Time{}
}
