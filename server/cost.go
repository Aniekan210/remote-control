package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// costTracker keeps running totals of what the AI calls have cost, for the
// current UTC day and month, so every call's log line can show where
// today's and this month's spend stand (across every key). The month
// spend on the SERVER's own key is what the MONTHLY_BUDGET_USD cap is
// checked against — users' own keys are theirs to limit — so it's
// persisted to a small JSON file (COST_STATE_FILE, default
// cost-state.json) after every call; otherwise a server restart would
// quietly reset the budget to $0.
type costTracker struct {
	mu          sync.Mutex
	path        string
	DayKey      string  `json:"day_key"`
	MonthKey    string  `json:"month_key"`
	Day         float64 `json:"day_usd"`
	Month       float64 `json:"month_usd"`
	ServerMonth float64 `json:"server_key_month_usd"`
}

var costs = &costTracker{}

// loadCostTracker restores the persisted totals, starting from $0 if the
// file doesn't exist yet or can't be read.
func loadCostTracker(path string) *costTracker {
	c := &costTracker{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			srvLogf("WARNING: could not read cost state %s: %v (starting from $0)", path, err)
		}
		return c
	}
	if err := json.Unmarshal(data, c); err != nil {
		srvLogf("WARNING: could not parse cost state %s: %v (starting from $0)", path, err)
		return &costTracker{path: path}
	}
	c.rollLocked(time.Now().UTC())
	srvLogf("cost state loaded from %s: today=$%.4f month=$%.4f (server key $%.4f)", path, c.Day, c.Month, c.ServerMonth)
	return c
}

// add records one call's cost and returns the updated day and month
// totals. serverKey marks a call billed to the server's own key.
func (c *costTracker) add(usd float64, serverKey bool) (day, month float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rollLocked(time.Now().UTC())
	c.Day += usd
	c.Month += usd
	if serverKey {
		c.ServerMonth += usd
	}
	c.saveLocked()
	return c.Day, c.Month
}

// serverMonthTotal returns this month's spend on the server's key so far.
func (c *costTracker) serverMonthTotal() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rollLocked(time.Now().UTC())
	return c.ServerMonth
}

// rollLocked resets a total when its day/month has ended. Caller holds mu.
func (c *costTracker) rollLocked(now time.Time) {
	if d := now.Format("2006-01-02"); d != c.DayKey {
		c.DayKey = d
		c.Day = 0
	}
	if m := now.Format("2006-01"); m != c.MonthKey {
		c.MonthKey = m
		c.Month = 0
		c.ServerMonth = 0
	}
}

// saveLocked writes the totals via a temp file + rename, so a crash
// mid-write can't leave a truncated file. Caller holds mu.
func (c *costTracker) saveLocked() {
	if c.path == "" {
		return
	}
	data, err := json.Marshal(c)
	if err != nil {
		return
	}
	if dir := filepath.Dir(c.path); dir != "." {
		_ = os.MkdirAll(dir, 0o755)
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		srvLogf("WARNING: could not save cost state: %v", err)
		return
	}
	if err := os.Rename(tmp, c.path); err != nil {
		srvLogf("WARNING: could not save cost state: %v", err)
	}
}
