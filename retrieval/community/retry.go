package community

import (
	"context"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"time"
)

type retryBudgetKey struct{}
type readRetryBudget struct {
	retries int
	waited  time.Duration
}

// Retry bounded busy responses or interrupted connections for safe reads and
// receipt-idempotent upload batches. Every attempt keeps the same
// origin, community, credential and payload. Never widen scope or reconnect to
// evade capacity controls. Other mutations and connection creation are not retried.
func (c *Client) call(ctx context.Context, path string, payload, out any) error {
	request, ok := payload.(Request)
	retryable := ok && (readOperation(request.Action) || request.Action == "knowledge.inventory" || request.Action == "knowledge.put")
	budget, _ := ctx.Value(retryBudgetKey{}).(*readRetryBudget)
	if budget == nil {
		budget = &readRetryBudget{}
	}
	// Calls within one Operation are sequential. Public credential renewal must
	// not replenish its busy-response retry allowance.
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := c.callOnce(ctx, path, payload, out)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var api *APIError
		var network *net.OpError
		interrupted := errors.As(err, &network) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
		if err == nil || !retryable || budget.retries >= 2 || (errors.As(err, &api) && api.Status != 429 && api.Status != 503) || (api == nil && !interrupted && request.Action != "knowledge.put") {
			return err
		}
		header := ""
		if api != nil {
			header = api.RetryAfter
		} else if interrupted {
			// Leave time for a managed application switch without a retry storm.
			header = strconv.Itoa(1 << budget.retries)
		}
		delay, valid := retryDelay(header, budget.retries, time.Now())
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
