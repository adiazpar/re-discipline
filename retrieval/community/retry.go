package community

import (
	"context"
	"errors"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"
)

type retryBudgetKey struct{}
type readRetryBudget struct {
	retries int
	waited  time.Duration
}

// Retry only explicit busy responses to reads. Every attempt keeps the same
// origin, community, credential and payload. Never widen scope or reconnect to
// evade capacity controls. Mutations and connection creation are not retried.
func (c *Client) call(ctx context.Context, path string, payload, out any) error {
	request, ok := payload.(Request)
	retryable := ok && readOperation(request.Action)
	budget, _ := ctx.Value(retryBudgetKey{}).(*readRetryBudget)
	if budget == nil {
		budget = &readRetryBudget{}
	}
	// Calls within one Operation are sequential. Public credential renewal must
	// not replenish its busy-response retry allowance.
	for {
		err := c.callOnce(ctx, path, payload, out)
		var api *APIError
		if err == nil || !retryable || budget.retries >= 2 || !errors.As(err, &api) || (api.Status != 429 && api.Status != 503) {
			return err
		}
		delay, valid := retryDelay(api.RetryAfter, budget.retries, time.Now())
		if !valid || budget.waited+delay > 5*time.Second {
			return err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
		budget.waited += delay
		budget.retries++
	}
}

func retryDelay(header string, attempt int, now time.Time) (time.Duration, bool) {
	delay := (250 * time.Millisecond) << attempt
	if header != "" {
		if seconds, err := strconv.ParseInt(header, 10, 64); err == nil {
			if seconds < 0 || seconds > 5 {
				return 0, false
			}
			delay = max(delay, time.Duration(seconds)*time.Second)
		} else if date, err := http.ParseTime(header); err == nil {
			delay = max(delay, date.Sub(now))
		} else {
			return 0, false
		}
	}
	// Bound a far-future HTTP date before adding jitter (Sub may saturate).
	if delay > 5*time.Second {
		return 0, false
	}
	// Positive jitter respects Retry-After and spreads agents' retry arrivals.
	return delay + time.Duration(rand.Int64N(int64(250*time.Millisecond))), true
}
