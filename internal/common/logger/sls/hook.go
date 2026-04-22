package sls

import (
	"fmt"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/aliyun/aliyun-log-go-sdk/producer"
	"github.com/sirupsen/logrus"
)

// SLSHook is a logrus hook for shipping logs to Alibaba Cloud SLS.
type SLSHook struct {
	producer   *producer.Producer
	project    string
	logstore   string
	topic      string
	source     string
	enabled    bool
	metadata   map[string]string
	metrics    *SLSMetrics
	mu         sync.RWMutex
	sampleRate int
}

// NewSLSHook creates a new SLS hook.
//sayso-lint:ignore godoc-error-undoc
func NewSLSHook(config *SLSHookConfig) (*SLSHook, error) {
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid SLS config: %w", err)
	}
	config.SetDefaults()

	producerConfig := producer.GetDefaultProducerConfig()
	producerConfig.Endpoint = config.Endpoint
	producerConfig.AccessKeyID = config.AccessKeyID
	producerConfig.AccessKeySecret = config.AccessKeySecret
	producerConfig.MaxBatchSize = int64(config.MaxBatchSize)
	producerConfig.MaxBatchCount = config.MaxBatchCount
	producerConfig.LingerMs = int64(config.LingerMs)
	producerConfig.Retries = config.Retries
	producerConfig.MaxReservedAttempts = config.MaxReservedAttempts

	slsProducer := producer.InitProducer(producerConfig)
	slsProducer.Start()

	metadata := make(map[string]string)
	for k, v := range config.Metadata {
		metadata[k] = v
	}
	if hostname, err := os.Hostname(); err == nil {
		metadata["hostname"] = hostname
	}
	metadata["pid"] = fmt.Sprintf("%d", os.Getpid())
	metadata["start_time"] = time.Now().UTC().Format(time.RFC3339)
	metadata["go_version"] = runtime.Version()
	metadata["os"] = runtime.GOOS
	metadata["arch"] = runtime.GOARCH

	return &SLSHook{
		producer:   slsProducer,
		project:    config.Project,
		logstore:   config.Logstore,
		topic:      config.Topic,
		source:     config.Source,
		enabled:    config.Enabled,
		metadata:   metadata,
		metrics:    &SLSMetrics{},
		sampleRate: 100,
	}, nil
}

// Fire ships the log entry to SLS; errors are counted but never returned
// so they don't interrupt the primary logging flow.
func (h *SLSHook) Fire(entry *logrus.Entry) error {
	//sayso-lint:ignore lock-without-defer
	h.mu.RLock()
	if !h.enabled {
		h.mu.RUnlock()
		return nil
	}
	h.mu.RUnlock()

	slsLog := entryToSLSLog(entry, h.metadata)
	err := h.producer.SendLog(h.project, h.logstore, h.topic, h.source, slsLog)

	h.metrics.IncrementTotal()
	if err != nil {
		h.metrics.IncrementFailed(err)

		//sayso-lint:ignore lock-without-defer
		h.mu.Lock()
		if h.metrics.FailedLogs%int64(h.sampleRate) == 0 {
			fmt.Fprintf(os.Stderr, "SLS send failed (total: %d, failed: %d): %v\n",
				h.metrics.TotalLogs, h.metrics.FailedLogs, err)
		}
		h.mu.Unlock()
		return nil
	}

	h.metrics.IncrementSuccess()
	return nil
}

// Levels ships every log level to SLS.
func (h *SLSHook) Levels() []logrus.Level {
	return logrus.AllLevels
}

// Close flushes pending logs then shuts the producer down.
//sayso-lint:ignore godoc-error-undoc
func (h *SLSHook) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.producer != nil {
		h.producer.SafeClose()
	}
	return nil
}

//sayso-lint:ignore unused-export
func (h *SLSHook) IsEnabled() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.enabled
}

//sayso-lint:ignore unused-export
func (h *SLSHook) Disable() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enabled = false
}

//sayso-lint:ignore unused-export
func (h *SLSHook) Enable() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.enabled = true
}

func (h *SLSHook) GetMetrics() map[string]interface{} {
	return h.metrics.GetMetrics()
}
