package main

import "time"

// ─── Tunable configuration ──────────────────────────────────────────────────

var cfg = struct {
	ParallelWorkers    int
	StaggerMinMS       int
	StaggerMaxMS       int
	HTTPTimeoutShort   time.Duration
	HTTPTimeoutMedium  time.Duration
	PollReceiptMax     int
	ShortSleep         time.Duration
	MaxWaitSeconds     float64
	MaxPrice           float64
	MaxPriceFallback   float64
	FastMode           bool
	SummaryOnly        bool
	HardcodedPhone     string
	SiteRemoval        bool
	SingleProxyAttempt bool
}{
	ParallelWorkers:    32,
	StaggerMinMS:       100,
	StaggerMaxMS:       300,
	HTTPTimeoutShort:   5 * time.Second,
	HTTPTimeoutMedium:  8 * time.Second,
	PollReceiptMax:     10,
	ShortSleep:         400 * time.Millisecond,
	MaxWaitSeconds:     1.5,
	MaxPrice:           50.0,
	MaxPriceFallback:   500.0,
	FastMode:           true,
	SummaryOnly:        true,
	HardcodedPhone:     "2494851515",
	SiteRemoval:        true,
	SingleProxyAttempt: true,
}
