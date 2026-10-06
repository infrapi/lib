package cluster

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/grafana/dskit/services"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/sirupsen/logrus"
)

// eligibility decides, for one leader-mode role, whether this replica may
// lead, and publishes the answer in the ring as the instance's read-only flag:
// in dskit's terms a read-only instance takes no writes, which is exactly a
// replica that must not be the writer. Role.Leader skips such instances.
//
// It is a dskit timer service started right after the role's lifecycler, so it
// stops before it, never publishing on a lifecycler that is gone.
type eligibility struct {
	role      *Role
	checks    []Check
	timeout   time.Duration
	threshold int
	logger    logrus.FieldLogger

	gauge    prometheus.Gauge
	failures *prometheus.CounterVec

	mu        sync.RWMutex
	published bool      // what the ring says: true = may lead
	reason    string    // the failing check and its error, when not
	checkedAt time.Time // the last evaluation

	// streakValue is the result seen on the last evaluations, streak how many
	// of them in a row: the published state flips only once streak reaches
	// threshold.
	streakValue bool
	streak      int
}

// service is the timer that evaluates every interval. The first evaluation is
// published at once, threshold or not: a replica must not lead for even one
// interval on a default it never checked.
func (e *eligibility) service(interval time.Duration) services.Service {
	return services.NewTimerService(interval, func(ctx context.Context) error {
		ok, reason := e.evaluate(ctx)
		e.mu.Lock()
		e.streakValue, e.streak = ok, e.threshold
		e.mu.Unlock()
		return e.publish(ctx, ok, reason)
	}, func(ctx context.Context) error {
		ok, reason := e.evaluate(ctx)
		e.mu.Lock()
		flip, value := nextState(e.published, e.streakValue, e.streak, ok, e.threshold)
		e.streakValue, e.streak = ok, streakAfter(e.streakValue, e.streak, ok)
		e.reason, e.checkedAt = reason, time.Now()
		e.mu.Unlock()
		if flip {
			if err := e.publish(ctx, value, reason); err != nil {
				// Logged, retried at the next tick: a check loop must not kill
				// the cluster.
				e.logger.WithError(err).Warn("cluster: could not publish leader eligibility")
			}
		}
		return nil
	}, nil)
}

// evaluate runs the checks once.
func (e *eligibility) evaluate(ctx context.Context) (bool, string) {
	failed, err := runChecks(ctx, e.checks, e.timeout)
	if err != nil {
		e.failures.WithLabelValues(e.role.spec.Name, failed.String()).Inc()
		return false, err.Error()
	}
	return true, ""
}

// publish writes the state into the ring and records it.
func (e *eligibility) publish(ctx context.Context, mayLead bool, reason string) error {
	if err := e.role.lifecycler.ChangeReadOnlyState(ctx, !mayLead); err != nil {
		return err
	}
	e.mu.Lock()
	changed := e.published != mayLead || e.checkedAt.IsZero()
	e.published, e.reason, e.checkedAt = mayLead, reason, time.Now()
	e.mu.Unlock()

	if mayLead {
		e.gauge.Set(1)
	} else {
		e.gauge.Set(0)
	}
	if changed {
		entry := e.logger.WithField("may_lead", mayLead)
		if reason != "" {
			entry = entry.WithField("reason", reason)
		}
		entry.Info("cluster: leader eligibility published")
	}
	return nil
}

// state is what the status pages show of this replica.
func (e *eligibility) state() (mayLead bool, reason string, checkedAt time.Time) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.published, e.reason, e.checkedAt
}

// nextState says whether the published state flips after a new result, and to
// what: only when threshold results in a row, this one included, disagree
// with what is published.
func nextState(published, streakValue bool, streak int, result bool, threshold int) (flip, value bool) {
	if result == published {
		return false, published
	}
	if streakAfter(streakValue, streak, result) >= threshold {
		return true, result
	}
	return false, published
}

// streakAfter counts the results in a row equal to the newest one.
func streakAfter(streakValue bool, streak int, result bool) int {
	if result == streakValue {
		return streak + 1
	}
	return 1
}

// leaderChecks is every check a leader-mode role runs: the configured ones,
// then the role's own.
func (r *Role) leaderChecks() []Check {
	checks := append([]Check{}, r.cluster.leaderChecks...)
	if r.spec.LeaderCheck != nil {
		checks = append(checks, CheckFunc(fmt.Sprintf("%s check", r.spec.Name), r.spec.LeaderCheck))
	}
	return checks
}
