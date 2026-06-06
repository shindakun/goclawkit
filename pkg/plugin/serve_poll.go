package plugin

import (
	"context"
	"time"
)

// defaultPollInterval is used when a Poller's Interval() is non-positive.
const defaultPollInterval = 60 * time.Second

// Poller is a channel whose inbound is produced by POLLING an upstream on an interval,
// rather than by holding a connection. ServePoll runs the loop (ticker, backoff, ctx,
// bridging to the channel.* protocol) so the author implements only Poll and Send.
//
// It is the poll-shaped sibling of Channel: where Channel.Start hands back a stream the
// author drives, a Poller just answers "what's new since last time" each tick.
//
// DEDUP is the author's responsibility: ServePoll has NO memory of what it already
// emitted, so Poll must return only genuinely-new items, or the agent is flooded with
// repeats every interval. A source you can mutate dedups by mutating it; e.g. Gmail
// marks messages read so the next query excludes them:
//
//	func (g *gmail) Poll(ctx context.Context) ([]plugin.Inbound, error) {
//	    msgs, err := g.listUnread(ctx) // q=is:unread, so seen mail is excluded next time
//	    if err != nil {
//	        return nil, err
//	    }
//	    var in []plugin.Inbound
//	    for _, m := range msgs {
//	        in = append(in, toInbound(m))
//	        g.markRead(ctx, m.ID) // mutate the source: the dedup
//	    }
//	    return in, nil // oldest-first; ServePoll preserves slice order
//	}
//
// A source you CANNOT mutate (e.g. an RSS feed) needs Poll to track seen ids itself.
type Poller interface {
	Info() Info
	// Interval is the time between polls, asked once by ServePoll at start. A
	// non-positive value uses the default (60s). Read it from env in your constructor
	// to keep config out of the SDK.
	Interval() time.Duration
	// Poll is called once per tick and returns the inbound messages that are NEW since
	// the previous successful Poll, in the order they should reach the agent (ServePoll
	// preserves slice order). Returning (nil, nil) is the normal "nothing new" case;
	// returning an error triggers ServePoll's backoff and the tick is retried later.
	// Dedup is the author's job (see the interface doc).
	Poll(ctx context.Context) ([]Inbound, error)
	// Send delivers one outbound reply, exactly as Channel.Send. It is called
	// concurrently with polling, so Send and Poll must be safe to run at once (e.g. if
	// both touch a shared HTTP client).
	Send(ctx context.Context, out Outbound) error
}

// ServePoll runs a Poller as a channel plugin: it owns the poll loop and bridges new
// Inbounds up the channel.* protocol, so the author writes only Poll + Send. It is a
// thin adapter over ServeChannel; the handshake announces Kind=channel like any
// channel, so the host cannot tell a poll channel from a hand-written one (correct: it
// is just a channel).
func ServePoll(p Poller) error {
	return ServeChannel(&pollChannel{p: p})
}

// pollChannel adapts a Poller to the Channel interface: Start spins the poll loop and
// feeds an inbound stream; Send and Info delegate straight through.
type pollChannel struct{ p Poller }

func (c *pollChannel) Info() Info { return c.p.Info() }

func (c *pollChannel) Send(ctx context.Context, out Outbound) error {
	return c.p.Send(ctx, out)
}

func (c *pollChannel) Start(ctx context.Context) (<-chan Inbound, error) {
	out := make(chan Inbound)
	interval := c.p.Interval()
	if interval <= 0 {
		interval = defaultPollInterval
	}
	name := c.p.Info().Name
	go func() {
		defer close(out)
		runPollLoop(ctx, c.p, interval, name, out)
	}()
	return out, nil
}

// runPollLoop is the loop body, factored out for testability. It polls immediately
// (so a fresh start is live at once, not after a full interval), then spaces polls by
// interval measured from each poll's START; on a Poll error it backs off with capped
// exponential backoff and resets to the base after any clean poll. Every iteration
// yields to ctx, so the loop can never busy-spin and unwinds promptly on cancel.
func runPollLoop(ctx context.Context, p Poller, interval time.Duration, name string, out chan<- Inbound) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		start := time.Now()

		items, err := p.Poll(ctx)
		if err != nil {
			Logf(name, "poll error (retry in %s): %v", backoff, err)
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = capDur(backoff*2, time.Minute)
			continue
		}
		backoff = time.Second // reset after a clean poll

		for _, in := range items {
			select {
			case out <- in:
			case <-ctx.Done():
				return
			}
		}

		// Wait out the remainder of the interval (so poll cost does not add to it). If
		// the poll already overran the interval, do not sleep, but STILL yield to ctx
		// each iteration so a perpetually-slow Poll cannot busy-spin.
		wait := interval - time.Since(start)
		if wait <= 0 {
			select {
			case <-ctx.Done():
				return
			default:
				continue
			}
		}
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

// sleepCtx sleeps for d unless ctx is cancelled first; it reports false if cancelled.
// (pkg/plugin's own copy; cmd/irc has an identical unexported one in package main.)
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

// capDur caps d at max.
func capDur(d, max time.Duration) time.Duration {
	if d > max {
		return max
	}
	return d
}
