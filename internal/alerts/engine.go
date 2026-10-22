/*
Package alerts schedules bounded evaluation from already available snapshots.

The engine owns no goroutine, performs no collection, and isolates each rule's
failure. Callers explicitly provide timing, transition policy, and observation.
*/
package alerts

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// SampleSource reads current evaluator samples without initiating collection.
type SampleSource interface {
	Samples(context.Context, string) (Samples, error)
}

// EngineConfig bounds evaluation work and defines Incident transitions.
type EngineConfig struct {
	Interval       time.Duration
	Timeout        time.Duration
	MaxRulesPerRun int
	IncidentPolicy IncidentPolicy
}

// EngineObservation reports one rule-page or rule operation.
type EngineObservation struct {
	DeviceID   string
	RuleID     string
	Revision   uint64
	Evaluation State
	Incident   IncidentStatus
	Duration   time.Duration
	Err        error
}

// EngineSummary describes bounded work completed by one run.
type EngineSummary struct {
	Attempted    int
	Succeeded    int
	Failed       int
	LimitReached bool
	Duration     time.Duration
}

// EngineObserver receives synchronous operational observations.
type EngineObserver func(EngineObservation)

// Engine evaluates current rule revisions serially with per-rule isolation.
type Engine struct {
	samples        SampleSource
	rules          EvaluationRuleRepository
	incidents      IncidentRepository
	evaluator      *Evaluator
	config         EngineConfig
	observe        EngineObserver
	now            func() time.Time
	mu             sync.Mutex
	cursorDeviceID string
	cursorRuleID   string
}

// NewEngine validates every dependency and all scheduler policy explicitly.
func NewEngine(samples SampleSource, rules EvaluationRuleRepository, incidents IncidentRepository, evaluator *Evaluator, config EngineConfig, observer EngineObserver) (*Engine, error) {
	if samples == nil || rules == nil || incidents == nil || evaluator == nil || observer == nil {
		return nil, errors.New("sample, rule, Incident, evaluator, and observer dependencies are required")
	}
	if config.Interval <= 0 || config.Timeout <= 0 || config.MaxRulesPerRun < 1 {
		return nil, errors.New("positive alert evaluation interval, timeout, and rule limit are required")
	}
	if err := ValidateIncidentPolicy(config.IncidentPolicy); err != nil {
		return nil, err
	}
	return &Engine{samples: samples, rules: rules, incidents: incidents, evaluator: evaluator, config: config, observe: observer, now: time.Now}, nil
}

// RunOnce evaluates at most MaxRulesPerRun current rules under one timeout.
func (e *Engine) RunOnce(ctx context.Context) EngineSummary {
	e.mu.Lock()
	defer e.mu.Unlock()
	started := e.now()
	summary := EngineSummary{}
	if ctx == nil {
		summary.Failed = 1
		e.observe(EngineObservation{Duration: e.now().Sub(started), Err: errors.New("alert evaluation context is required")})
		summary.Duration = e.now().Sub(started)
		return summary
	}
	runContext, cancel := context.WithTimeout(ctx, e.config.Timeout)
	defer cancel()
	rules, err := e.evaluationPage(runContext)
	if err != nil {
		summary.Failed++
		e.observe(EngineObservation{Duration: e.now().Sub(started), Err: fmt.Errorf("list current alert rules: %w", err)})
		summary.Duration = e.now().Sub(started)
		return summary
	}
	summary.LimitReached = len(rules) == e.config.MaxRulesPerRun
	samplesByDevice := make(map[string]Samples)
	sampleErrors := make(map[string]error)
	loadedDevices := make(map[string]bool)
	for _, rule := range rules {
		summary.Attempted++
		operationStarted := e.now()
		current, found, err := e.incidents.Current(runContext, rule.DeviceID, rule.ID)
		if err == nil && found && current.RuleRevision != rule.Revision {
			err = ErrIncidentRevision
		}
		var result EvaluationResult
		if err == nil {
			if rule.Enabled && !loadedDevices[rule.DeviceID] {
				samplesByDevice[rule.DeviceID], sampleErrors[rule.DeviceID] = e.samples.Samples(runContext, rule.DeviceID)
				loadedDevices[rule.DeviceID] = true
			}
			if sampleErrors[rule.DeviceID] != nil {
				err = sampleErrors[rule.DeviceID]
			} else {
				result, err = e.evaluator.Evaluate(runContext, rule, samplesByDevice[rule.DeviceID])
			}
		}
		var next IncidentState
		if err == nil {
			expected := time.Time{}
			if found {
				expected = current.LastEvaluatedAt
			}
			next, err = e.incidents.Advance(runContext, rule, result, e.now().UTC(), e.config.IncidentPolicy, expected)
		}
		observation := EngineObservation{DeviceID: rule.DeviceID, RuleID: rule.ID, Revision: rule.Revision, Evaluation: result.State, Incident: next.Status, Duration: e.now().Sub(operationStarted), Err: err}
		if err != nil {
			summary.Failed++
		} else {
			summary.Succeeded++
		}
		e.observe(observation)
		e.cursorDeviceID, e.cursorRuleID = rule.DeviceID, rule.ID
	}
	summary.Duration = e.now().Sub(started)
	return summary
}

func (e *Engine) evaluationPage(ctx context.Context) ([]Rule, error) {
	limit := e.config.MaxRulesPerRun
	rules, err := e.rules.ListEvaluationPage(ctx, e.cursorDeviceID, e.cursorRuleID, limit)
	if err != nil || len(rules) == limit || e.cursorDeviceID == "" {
		return rules, err
	}
	wrapped, err := e.rules.ListEvaluationPage(ctx, "", "", limit-len(rules))
	if err != nil {
		return nil, err
	}
	for _, rule := range wrapped {
		if rule.DeviceID > e.cursorDeviceID || (rule.DeviceID == e.cursorDeviceID && rule.ID > e.cursorRuleID) {
			break
		}
		rules = append(rules, rule)
	}
	return rules, nil
}

// Run evaluates immediately and then on each configured interval until canceled.
func (e *Engine) Run(ctx context.Context) {
	if ctx == nil {
		return
	}
	e.RunOnce(ctx)
	ticker := time.NewTicker(e.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.RunOnce(ctx)
		}
	}
}
