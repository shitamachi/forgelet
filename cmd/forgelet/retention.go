package main

import (
	"fmt"
	"time"

	"github.com/shitamachi/forgelet/internal/server"
)

type retentionConfig struct {
	History  string `yaml:"history"`
	Logs     string `yaml:"logs"`
	Events   string `yaml:"events"`
	Backups  string `yaml:"backups"`
	Interval string `yaml:"interval"`
}

func defaultRetentionConfig() retentionConfig {
	return retentionConfig{History: "720h", Logs: "168h", Events: "720h", Backups: "168h", Interval: "10m"}
}

func (c retentionConfig) policy() (server.Retention, error) {
	var r server.Retention
	for _, field := range []struct {
		name, raw string
		dest      *time.Duration
	}{{"history", c.History, &r.History}, {"logs", c.Logs, &r.Logs}, {"events", c.Events, &r.Events}, {"backups", c.Backups, &r.Backups}, {"interval", c.Interval, &r.Interval}} {
		value, err := time.ParseDuration(field.raw)
		if err != nil {
			return r, fmt.Errorf("retention.%s: %w", field.name, err)
		}
		*field.dest = value
	}
	return r, r.Validate()
}
