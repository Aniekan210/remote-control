package main

import (
	"os"
	"strconv"
	"time"
)

// Limits are the spending/runaway caps. Loaded once from the environment
// at startup (see loadLimits); package-level so the state logic can read
// them without any I/O.
type Limits struct {
	MaxAICallsPerTask      int           // MAX_AI_CALLS_PER_TASK
	MaxPlannerCallsPerTask int           // MAX_PLANNER_CALLS_PER_TASK (first plan + replans)
	MaxTaskCostUSD         float64       // MAX_TASK_COST_USD
	MaxTaskDuration        time.Duration // MAX_TASK_DURATION ("10m", or plain minutes)
	MonthlyBudgetUSD       float64       // MONTHLY_BUDGET_USD — the server's own key only
}

var limits = defaultLimits()

func defaultLimits() Limits {
	return Limits{
		// Granular plans mean many cheap executor calls, so the call cap is
		// generous; the planner cap is what actually bounds cost.
		MaxAICallsPerTask:      50,
		MaxPlannerCallsPerTask: 4,
		MaxTaskCostUSD:         0.10,
		MaxTaskDuration:        10 * time.Minute,
		MonthlyBudgetUSD:       6,
	}
}

func loadLimits() Limits {
	l := defaultLimits()
	l.MaxAICallsPerTask = envInt("MAX_AI_CALLS_PER_TASK", l.MaxAICallsPerTask)
	l.MaxPlannerCallsPerTask = envInt("MAX_PLANNER_CALLS_PER_TASK", l.MaxPlannerCallsPerTask)
	l.MaxTaskCostUSD = envFloat("MAX_TASK_COST_USD", l.MaxTaskCostUSD)
	l.MonthlyBudgetUSD = envFloat("MONTHLY_BUDGET_USD", l.MonthlyBudgetUSD)
	if v := os.Getenv("MAX_TASK_DURATION"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			l.MaxTaskDuration = d
		} else if m, err := strconv.Atoi(v); err == nil && m > 0 {
			l.MaxTaskDuration = time.Duration(m) * time.Minute
		}
	}
	return l
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			return f
		}
	}
	return def
}
