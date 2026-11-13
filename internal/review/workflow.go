// Package review holds the PRReview and ReviewRound workflows and their
// activities (§7). Workflow inputs and state hold IDs only (D-01).
package review

import (
	"errors"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/alternayte/sayso/internal/review/signals"
)

// RoundWorkflowType is the type name of the round child workflow.
const RoundWorkflowType = "ReviewRound"

// WorkWorkflowType is the type name of the reply and fix child workflow.
const WorkWorkflowType = "AgentWork"

// commandsVersion marks the PRReview code that handles commands, replies
// and human comments (S7). Histories without it drop those signals.
const commandsVersion = "commands"

// MaxSignals is the signal count after which PRReview continues as new (§7.1).
const MaxSignals = 200

// operatorReason is the cancel reason when the Temporal CLI cancels a round.
const operatorReason = "Cancelled by operator"

// State is the PRReview state. It survives continue-as-new.
// Start is embedded, so the webhook's signal-with-start input decodes as a
// first State.
type State struct {
	signals.Start
	ReviewID uuid.UUID `json:"review_id"`
	// Paused is the pause command state (/sayso pause).
	Paused bool `json:"paused"`
	// Pending is a head update that waits for the debounce timer.
	Pending *signals.Signal `json:"pending,omitempty"`
	// Signals counts the signals of this run, for continue-as-new.
	Signals int `json:"signals"`
	// Queue holds reply and fix work that waits for the running round (§7.1).
	Queue []uuid.UUID `json:"queue,omitempty"`
}

// RoundInput is the input of ReviewRound.
type RoundInput struct {
	RoundID      uuid.UUID `json:"round_id"`
	RepositoryID uuid.UUID `json:"repository_id"`
}

var act *Activities
var agent *AgentActivities

func shortOptions(ctx workflow.Context) workflow.Context {
	return workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{InitialInterval: time.Second, BackoffCoefficient: 2, MaximumInterval: time.Minute, MaximumAttempts: 6},
	})
}

// ReviewRound runs one round (§7.2): resolve the config, mark it started,
// wait for an agent run slot, run the agent, reconcile the ledger, publish
// and report to Skael. A failure sets a neutral check and the status line.
func ReviewRound(ctx workflow.Context, in RoundInput) (err error) {
	sctx := shortOptions(ctx)
	finish := func(status, reason string) error {
		dctx, _ := workflow.NewDisconnectedContext(sctx)
		return workflow.ExecuteActivity(dctx, act.FinishRound, Finish{RoundID: in.RoundID, Status: status, Reason: reason}).Get(dctx, nil)
	}
	defer func() {
		if err == nil {
			return
		}
		if temporal.IsCanceledError(err) || errors.Is(ctx.Err(), workflow.ErrCanceled) {
			_ = finish("cancelled", operatorReason)
			return
		}
		_ = finish("failed", reasonOf(err))
		err = nil
	}()

	if err := workflow.ExecuteActivity(sctx, act.ResolveConfig, in.RoundID).Get(sctx, nil); err != nil {
		return err
	}
	if err := workflow.ExecuteActivity(sctx, act.MarkStarted, in.RoundID).Get(sctx, nil); err != nil {
		return err
	}
	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 24 * time.Hour, HeartbeatTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second},
	})
	var admission uuid.UUID
	if err := workflow.ExecuteActivity(actx, act.Admit, AdmitInput{RoundID: &in.RoundID, RepositoryID: in.RepositoryID, Kind: KindRound}).Get(actx, &admission); err != nil {
		return err
	}
	var timeoutMin int
	if err := workflow.ExecuteActivity(sctx, act.RoundTimeout, in.RoundID).Get(sctx, &timeoutMin); err != nil {
		return err
	}
	rctx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue:           signals.AgentRunsQueue,
		StartToCloseTimeout: time.Duration(timeoutMin)*time.Minute + 5*time.Minute,
		HeartbeatTimeout:    time.Minute,
		WaitForCancellation: true,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 2},
	})
	runErr := workflow.ExecuteActivity(rctx, agent.RunAgent, RunAgentInput{RoundID: in.RoundID, AdmissionID: admission}).Get(rctx, nil)
	dctx, _ := workflow.NewDisconnectedContext(sctx)
	_ = workflow.ExecuteActivity(dctx, act.Release, admission).Get(dctx, nil)
	if runErr != nil {
		return runErr
	}
	var rec Reconciled
	if err := workflow.ExecuteActivity(sctx, act.ReconcileLedger, in.RoundID).Get(sctx, &rec); err != nil {
		return err
	}
	if rec.Status != "" {
		return finish(rec.Status, rec.Reason)
	}
	if err := workflow.ExecuteActivity(sctx, act.Publish, in.RoundID).Get(sctx, nil); err != nil {
		return err
	}
	// A Skael failure is recorded for Diagnostics; it never fails the round.
	_ = workflow.ExecuteActivity(sctx, act.ReportSkael, in.RoundID).Get(sctx, nil)
	return nil
}

func reasonOf(err error) string {
	var app *temporal.ApplicationError
	if errors.As(err, &app) && app.Message() != "" {
		return app.Message()
	}
	var timeout *temporal.TimeoutError
	if errors.As(err, &timeout) {
		return "the round timed out"
	}
	return err.Error()
}

// AgentWork runs one reply or fix run and posts its outcome (§11).
func AgentWork(ctx workflow.Context, in WorkInput) (err error) {
	sctx := shortOptions(ctx)
	defer func() {
		if err != nil {
			dctx, _ := workflow.NewDisconnectedContext(sctx)
			_ = workflow.ExecuteActivity(dctx, act.FailWork, in.WorkID, reasonOf(err)).Get(dctx, nil)
			err = nil
		}
	}()
	if err := workflow.ExecuteActivity(sctx, act.PrepareWork, in.WorkID).Get(sctx, nil); err != nil {
		return err
	}
	var status string
	if err := workflow.ExecuteActivity(sctx, act.WorkStatus, in.WorkID).Get(sctx, &status); err != nil || status != "running" {
		return err
	}
	actx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 24 * time.Hour, HeartbeatTimeout: 30 * time.Second,
		RetryPolicy: &temporal.RetryPolicy{InitialInterval: time.Second, MaximumInterval: 10 * time.Second},
	})
	var admission uuid.UUID
	if err := workflow.ExecuteActivity(actx, act.Admit, AdmitInput{WorkID: &in.WorkID, RepositoryID: in.RepositoryID, Kind: in.Kind}).Get(actx, &admission); err != nil {
		return err
	}
	var timeoutMin int
	if err := workflow.ExecuteActivity(sctx, act.ReplyTimeout, in.WorkID).Get(sctx, &timeoutMin); err != nil {
		return err
	}
	rctx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		TaskQueue: signals.AgentRunsQueue, StartToCloseTimeout: time.Duration(timeoutMin)*time.Minute + 5*time.Minute,
		HeartbeatTimeout: time.Minute, WaitForCancellation: true, RetryPolicy: &temporal.RetryPolicy{MaximumAttempts: 2},
	})
	runErr := workflow.ExecuteActivity(rctx, agent.RunAgent, RunAgentInput{WorkID: &in.WorkID, AdmissionID: admission}).Get(rctx, nil)
	dctx, _ := workflow.NewDisconnectedContext(sctx)
	_ = workflow.ExecuteActivity(dctx, act.Release, admission).Get(dctx, nil)
	if runErr != nil {
		return runErr
	}
	return workflow.ExecuteActivity(sctx, act.ApplyWork, in.WorkID).Get(sctx, nil)
}

// running is the round child that runs now.
type running struct {
	id     uuid.UUID
	future workflow.ChildWorkflowFuture
	cancel workflow.CancelFunc
}

// PRReview is the workflow of one pull request (§7.1).
func PRReview(ctx workflow.Context, st State) error {
	sctx := shortOptions(ctx)
	log := workflow.GetLogger(ctx)
	if st.ReviewID == uuid.Nil {
		if err := workflow.ExecuteActivity(sctx, act.EnsureReview, st.Start).Get(sctx, &st.ReviewID); err != nil {
			return err
		}
	}
	var debounce int
	if err := workflow.ExecuteActivity(sctx, act.Debounce).Get(sctx, &debounce); err != nil {
		return err
	}
	commands := workflow.GetVersion(ctx, commandsVersion, workflow.DefaultVersion, 1) >= 1
	var current *running
	var currentWork *running
	var timer workflow.Future
	var timerCancel workflow.CancelFunc
	closed := false
	var closeSignal signals.Signal

	startTimer := func() {
		if timerCancel != nil {
			timerCancel()
		}
		tctx, cancel := workflow.WithCancel(ctx)
		timer, timerCancel = workflow.NewTimer(tctx, time.Duration(debounce)*time.Second), cancel
	}
	cancelCurrent := func(reason string) {
		if current == nil {
			return
		}
		_ = workflow.ExecuteActivity(sctx, act.SetCancelReason, current.id, reason).Get(sctx, nil)
		current.cancel()
	}
	startRound := func(s signals.Signal) {
		// A reply or fix run finishes first; the round starts after it.
		if currentWork != nil {
			st.Pending = &s
			return
		}
		var d Decision
		in := DecideInput{ReviewID: st.ReviewID, HeadSHA: s.HeadSHA, BaseSHA: s.BaseSHA, Trigger: s.Trigger, Paused: st.Paused}
		if err := workflow.ExecuteActivity(sctx, act.Decide, in).Get(sctx, &d); err != nil {
			log.Error("decide failed", "error", err)
			return
		}
		state := "open"
		if d.Paused {
			state = "paused"
		}
		_ = workflow.ExecuteActivity(sctx, act.SetReviewState, st.ReviewID, state).Get(sctx, nil)
		if d.RoundID == nil {
			log.Info("no round", "reason", d.Reason)
			return
		}
		if current != nil {
			cancelCurrent("Cancelled for a newer commit")
		}
		cctx, cancel := workflow.WithCancel(ctx)
		cctx = workflow.WithChildOptions(cctx, workflow.ChildWorkflowOptions{
			WorkflowID: "round/" + d.RoundID.String(), TaskQueue: signals.TaskQueue,
			WaitForCancellation: true,
		})
		f := workflow.ExecuteChildWorkflow(cctx, RoundWorkflowType, RoundInput{RoundID: *d.RoundID, RepositoryID: st.RepositoryID})
		current = &running{id: *d.RoundID, future: f, cancel: cancel}
	}
	startWork := func() {
		if !commands || current != nil || currentWork != nil || len(st.Queue) == 0 {
			return
		}
		id := st.Queue[0]
		st.Queue = st.Queue[1:]
		var kind string
		if err := workflow.ExecuteActivity(sctx, act.WorkKind, id).Get(sctx, &kind); err != nil {
			return
		}
		cctx, cancel := workflow.WithCancel(ctx)
		cctx = workflow.WithChildOptions(cctx, workflow.ChildWorkflowOptions{WorkflowID: "work/" + id.String(), TaskQueue: signals.TaskQueue, WaitForCancellation: true})
		f := workflow.ExecuteChildWorkflow(cctx, WorkWorkflowType, WorkInput{WorkID: id, RepositoryID: st.RepositoryID, Kind: kind})
		currentWork = &running{id: id, future: f, cancel: cancel}
	}
	// apply carries out what a command or reply asked for.
	apply := func(o CommandOutcome) {
		switch o.Action {
		case ActionRound:
			startRound(signals.Signal{Trigger: signals.TriggerCommand})
		case ActionPause:
			st.Paused = true
			_ = workflow.ExecuteActivity(sctx, act.SetReviewState, st.ReviewID, "paused").Get(sctx, nil)
		case ActionResume:
			st.Paused = false
			_ = workflow.ExecuteActivity(sctx, act.SetReviewState, st.ReviewID, "open").Get(sctx, nil)
		case ActionWork:
			if o.WorkID != nil {
				st.Queue = append(st.Queue, *o.WorkID)
			}
		}
		startWork()
	}
	if st.Pending != nil {
		startTimer()
	}
	startWork()

	ch := func(name string) workflow.ReceiveChannel { return workflow.GetSignalChannel(ctx, name) }
	head, draft, closeCh, threads, cancelRound := ch(signals.SignalHeadUpdated), ch(signals.SignalPausedDraft), ch(signals.SignalClosed),
		ch(signals.SignalThreadState), ch(signals.SignalCancelRound)
	// Commands, replies and human comments arrive here; the commands of
	// §11 handle them (S7).
	later := []workflow.ReceiveChannel{ch(signals.SignalCommand), ch(signals.SignalThreadReply), ch(signals.SignalHumanComment)}

	for {
		sel := workflow.NewSelector(ctx)
		sel.AddReceive(head, func(c workflow.ReceiveChannel, _ bool) {
			var s signals.Signal
			c.Receive(ctx, &s)
			st.Signals++
			if s.Trigger == signals.TriggerCommand || s.Trigger == signals.TriggerCheckRerequest {
				startRound(s)
				return
			}
			st.Pending = &s
			startTimer()
		})
		sel.AddReceive(draft, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, nil)
			st.Signals++
			_ = workflow.ExecuteActivity(sctx, act.SetReviewState, st.ReviewID, "paused").Get(sctx, nil)
		})
		sel.AddReceive(closeCh, func(c workflow.ReceiveChannel, _ bool) {
			c.Receive(ctx, &closeSignal)
			st.Signals++
			closed = true
		})
		sel.AddReceive(threads, func(c workflow.ReceiveChannel, _ bool) {
			var s signals.Signal
			c.Receive(ctx, &s)
			st.Signals++
			_ = workflow.ExecuteActivity(sctx, act.ThreadState, st.ReviewID, s.ThreadNodeID, s.CommentID, s.Resolved, s.Actor).Get(sctx, nil)
		})
		sel.AddReceive(cancelRound, func(c workflow.ReceiveChannel, _ bool) {
			var s signals.Signal
			c.Receive(ctx, &s)
			st.Signals++
			if current != nil && current.id.String() == s.RoundID {
				cancelCurrent("Cancelled by @" + s.Actor)
			}
		})
		for i, c := range later {
			sel.AddReceive(c, func(c workflow.ReceiveChannel, _ bool) {
				var s signals.Signal
				c.Receive(ctx, &s)
				st.Signals++
				if !commands {
					return
				}
				in := CommentInput{ReviewID: st.ReviewID, CommentID: s.CommentID, Source: s.CommentSource, InReplyTo: s.InReplyTo}
				var o CommandOutcome
				var err error
				switch i {
				case 0:
					err = workflow.ExecuteActivity(sctx, act.HandleCommand, in).Get(sctx, &o)
				case 1:
					err = workflow.ExecuteActivity(sctx, act.HandleThreadReply, in).Get(sctx, &o)
				default:
					err = workflow.ExecuteActivity(sctx, act.HandleHumanComment, in).Get(sctx, nil)
				}
				if err != nil {
					log.Error("comment signal failed", "error", err)
					return
				}
				apply(o)
			})
		}
		if timer != nil {
			sel.AddFuture(timer, func(f workflow.Future) {
				timer, timerCancel = nil, nil
				if f.Get(ctx, nil) != nil || st.Pending == nil {
					return
				}
				p := *st.Pending
				st.Pending = nil
				startRound(p)
			})
		}
		if current != nil {
			cur := current
			sel.AddFuture(cur.future, func(f workflow.Future) {
				_ = f.Get(ctx, nil)
				if current == cur {
					current = nil
				}
				startWork()
			})
		}
		if currentWork != nil {
			w := currentWork
			sel.AddFuture(w.future, func(f workflow.Future) {
				_ = f.Get(ctx, nil)
				if currentWork == w {
					currentWork = nil
				}
				if st.Pending != nil && timer == nil {
					p := *st.Pending
					st.Pending = nil
					startRound(p)
				}
				startWork()
			})
		}
		sel.Select(ctx)

		if ctx.Err() != nil {
			// The Temporal CLI cancelled the review: stop the round the same way.
			dctx, _ := workflow.NewDisconnectedContext(sctx)
			if current != nil {
				_ = workflow.ExecuteActivity(dctx, act.SetCancelReason, current.id, operatorReason).Get(dctx, nil)
				current.cancel()
				_ = current.future.Get(dctx, nil)
			}
			return ctx.Err()
		}
		if closed {
			if timerCancel != nil {
				timerCancel()
			}
			if current != nil {
				cancelCurrent("Cancelled: the pull request is closed")
				_ = current.future.Get(ctx, nil)
				current = nil
			}
			if currentWork != nil {
				currentWork.cancel()
				_ = currentWork.future.Get(ctx, nil)
				currentWork = nil
			}
			state := "closed"
			if closeSignal.Merged {
				state = "merged"
			}
			if err := workflow.ExecuteActivity(sctx, act.SetReviewState, st.ReviewID, state).Get(sctx, nil); err != nil {
				return err
			}
			_ = workflow.ExecuteActivity(sctx, act.ReportReview, st.ReviewID).Get(sctx, nil)
			return nil
		}
		if st.Signals >= MaxSignals && current == nil && currentWork == nil {
			// Drain what arrived, so no signal is lost (§7.1).
			drained := false
			for _, c := range append([]workflow.ReceiveChannel{head, draft, closeCh, threads, cancelRound}, later...) {
				if c.Len() > 0 {
					drained = true
				}
			}
			if !drained {
				st.Signals = 0
				return workflow.NewContinueAsNewError(ctx, PRReview, st)
			}
		}
	}
}
