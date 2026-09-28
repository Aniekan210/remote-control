package main

import (
	"sync"
	"time"
)

// costTracker keeps running totals of what the AI calls have cost, for the
// current UTC day and month, so every call's log line can show where
// today's and this month's spend stand.
type costTracker struct {
	mu       sync.Mutex
	dayKey   string
	monthKey string
	day      float64
	month    float64
}

var costs = &costTracker{}

// add records one call's cost and returns the updated day and month totals.
func (c *costTracker) add(usd float64) (day, month float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rollLocked(time.Now().UTC())
	c.day += usd
	c.month += usd
	return c.day, c.month
}

// rollLocked resets a total when its day/month has ended. Caller holds mu.
func (c *costTracker) rollLocked(now time.Time) {
	if d := now.Format("2006-01-02"); d != c.dayKey {
		c.dayKey = d
		c.day = 0
	}
	if m := now.Format("2006-01"); m != c.monthKey {
		c.monthKey = m
		c.month = 0
	}
}
