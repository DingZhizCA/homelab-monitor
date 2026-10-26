/*
Package notifications schedules bounded processor passes without overlapping
runs. The caller owns lifecycle cancellation and supplies every timing limit.
*/
package notifications

import (
	"context"
	"errors"
	"time"
)

type EngineConfig struct {
	Interval time.Duration
	Timeout  time.Duration
}

type EngineObservation struct {
	Result   ProcessResult
	Duration time.Duration
	Err      error
}

type EngineObserver func(EngineObservation)

type Engine struct {
	processor *Processor
	config    EngineConfig
	observe   EngineObserver
	now       func() time.Time
}

func NewEngine(processor *Processor, config EngineConfig, observe EngineObserver, now func() time.Time) (*Engine, error) {
	if processor == nil || config.Interval <= 0 || config.Timeout <= 0 || observe == nil || now == nil {
		return nil, errors.New("notification engine requires processor, positive timing, observer, and clock")
	}
	return &Engine{processor: processor, config: config, observe: observe, now: now}, nil
}

func (e *Engine) RunOnce(ctx context.Context) ProcessResult {
	started := e.now()
	runContext, cancel := context.WithTimeout(ctx, e.config.Timeout)
	defer cancel()
	result, err := e.processor.RunOnce(runContext)
	e.observe(EngineObservation{Result: result, Duration: e.now().Sub(started), Err: err})
	return result
}

func (e *Engine) Run(ctx context.Context) {
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
