package maint

import (
	"context"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/go-faster/errors"
	"github.com/stretchr/testify/require"
)

// runScheduler starts s in the background and returns a stop function that
// cancels it and waits for Run to return.
func runScheduler(t *testing.T, s *Scheduler) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	return func() {
		cancel()
		require.NoError(t, <-done)
	}
}

// TestSchedulerFiresAfterDelayThenInterval pins the two timings that matter:
// the first pass waits StartDelay, not Interval, and every pass after it waits
// Interval.
func TestSchedulerFiresAfterDelayThenInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var runs atomic.Int64
		s, err := NewScheduler([]Job{{
			Name:     "job",
			Interval: time.Hour,
			Run: func(context.Context) error {
				runs.Add(1)
				return nil
			},
		}}, SchedulerOptions{StartDelay: time.Minute})
		require.NoError(t, err)

		stop := runScheduler(t, s)
		defer stop()

		time.Sleep(59 * time.Second)
		synctest.Wait()
		require.Zero(t, runs.Load(), "must not run before the start delay")

		time.Sleep(2 * time.Second)
		synctest.Wait()
		require.Equal(t, int64(1), runs.Load())

		time.Sleep(time.Hour)
		synctest.Wait()
		require.Equal(t, int64(2), runs.Load())
	})
}

// TestSchedulerSurvivesFailingJob pins that one broken sweep neither stops its
// own loop nor the other jobs: maintenance is a background duty, and a job that
// fails today may well succeed tomorrow.
func TestSchedulerSurvivesFailingJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var failed, healthy atomic.Int64
		s, err := NewScheduler([]Job{
			{Name: "broken", Interval: time.Hour, Run: func(context.Context) error {
				failed.Add(1)
				return errors.New("boom")
			}},
			{Name: "healthy", Interval: time.Hour, Run: func(context.Context) error {
				healthy.Add(1)
				return nil
			}},
		}, SchedulerOptions{StartDelay: time.Minute})
		require.NoError(t, err)

		stop := runScheduler(t, s)
		defer stop()

		time.Sleep(time.Minute + time.Hour + time.Second)
		synctest.Wait()
		require.Equal(t, int64(2), failed.Load(), "a failing job must keep its schedule")
		require.Equal(t, int64(2), healthy.Load())
	})
}

// TestSchedulerCancelsRunningJob pins that shutdown cancels a job's context
// rather than waiting it out.
func TestSchedulerCancelsRunningJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		var canceled atomic.Bool
		s, err := NewScheduler([]Job{{
			Name:     "slow",
			Interval: time.Hour,
			Run: func(ctx context.Context) error {
				close(started)
				<-ctx.Done()
				canceled.Store(true)
				return ctx.Err()
			},
		}}, SchedulerOptions{StartDelay: time.Minute, DrainTimeout: time.Minute})
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()

		time.Sleep(2 * time.Minute)
		synctest.Wait()
		<-started

		cancel()
		require.NoError(t, <-done)
		require.True(t, canceled.Load(), "the job must observe cancellation")
	})
}

// TestSchedulerDrainDeadlineReleases pins the backstop: a job that ignores
// cancellation must not hold shutdown open past DrainTimeout.
func TestSchedulerDrainDeadlineReleases(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })

		s, err := NewScheduler([]Job{{
			Name:     "stuck",
			Interval: time.Hour,
			Run: func(context.Context) error {
				<-release
				return nil
			},
		}}, SchedulerOptions{StartDelay: time.Minute, DrainTimeout: 30 * time.Second})
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()

		time.Sleep(2 * time.Minute)
		synctest.Wait()

		cancel()
		select {
		case err := <-done:
			require.NoError(t, err)
		case <-time.After(time.Minute):
			t.Fatal("Run did not return within the drain deadline")
		}
	})
}

func TestNewSchedulerDropsDisabledJobs(t *testing.T) {
	s, err := NewScheduler([]Job{
		{Name: "on", Interval: time.Hour, Run: func(context.Context) error { return nil }},
		{Name: "off", Interval: 0, Run: func(context.Context) error { return nil }},
	}, SchedulerOptions{})
	require.NoError(t, err)
	require.Len(t, s.Jobs(), 1)
	require.Equal(t, "on", s.Jobs()[0].Name)
}

func TestNewSchedulerRejectsInvalidJobs(t *testing.T) {
	run := func(context.Context) error { return nil }

	for _, tt := range []struct {
		name string
		jobs []Job
	}{
		{"no name", []Job{{Interval: time.Hour, Run: run}}},
		{"no run", []Job{{Name: "j", Interval: time.Hour}}},
		{"duplicate", []Job{
			{Name: "j", Interval: time.Hour, Run: run},
			{Name: "j", Interval: time.Hour, Run: run},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewScheduler(tt.jobs, SchedulerOptions{})
			require.Error(t, err)
		})
	}
}

// TestSchedulerWithNoJobsIdles pins that a fully disabled config is a running
// process that does nothing, not an error and not an exit.
func TestSchedulerWithNoJobsIdles(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, err := NewScheduler(nil, SchedulerOptions{})
		require.NoError(t, err)

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Run(ctx) }()

		time.Sleep(time.Hour)
		synctest.Wait()
		select {
		case <-done:
			t.Fatal("Run returned before its context was canceled")
		default:
		}

		cancel()
		require.NoError(t, <-done)
	})
}
