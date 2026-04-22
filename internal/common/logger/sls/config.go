package sls

import "fmt"

// SLSHookConfig holds configuration for SLS log shipping.
type SLSHookConfig struct {
	Enabled             bool
	Endpoint            string
	AccessKeyID         string
	AccessKeySecret     string
	Project             string
	Logstore            string
	Topic               string
	Source              string
	MaxBatchSize        int
	MaxBatchCount       int
	LingerMs            int
	Retries             int
	MaxReservedAttempts int
	Metadata            map[string]string
}

// Validate validates the SLS hook configuration.
//sayso-lint:ignore godoc-error-undoc
func (c *SLSHookConfig) Validate() error {
	if !c.Enabled {
		return fmt.Errorf("SLS hook is not enabled")
	}
	if c.Endpoint == "" {
		return fmt.Errorf("SLS endpoint is required")
	}
	if c.AccessKeyID == "" {
		return fmt.Errorf("SLS access key ID is required")
	}
	if c.AccessKeySecret == "" {
		return fmt.Errorf("SLS access key secret is required")
	}
	if c.Project == "" {
		return fmt.Errorf("SLS project is required")
	}
	if c.Logstore == "" {
		return fmt.Errorf("SLS logstore is required")
	}
	return nil
}

// SetDefaults sets default values for the configuration.
func (c *SLSHookConfig) SetDefaults() {
	if c.MaxBatchSize == 0 {
		c.MaxBatchSize = 3145728 // 3MB
	}
	if c.MaxBatchCount == 0 {
		c.MaxBatchCount = 4096
	}
	if c.LingerMs == 0 {
		c.LingerMs = 2000
	}
	if c.Retries == 0 {
		c.Retries = 3
	}
	if c.MaxReservedAttempts == 0 {
		c.MaxReservedAttempts = 11
	}
	if c.Topic == "" {
		c.Topic = "default"
	}
	if c.Source == "" {
		c.Source = "synapse"
	}
	if c.Metadata == nil {
		c.Metadata = make(map[string]string)
	}
}
