package review

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/activity"

	"github.com/alternayte/sayso/internal/settings"
)

// Admission kinds; reply and fix runs go first (§7.3).
const (
	KindRound = "round"
	KindReply = "reply"
	KindFix   = "fix"
)

// admissionLock serializes admission decisions across workers.
const admissionLock = 0x5a750001

// staleAfter releases a slot whose activity stopped heartbeating.
const staleAfter = time.Minute

// AdmitInput names the work that waits for a slot.
type AdmitInput struct {
	RoundID      *uuid.UUID `json:"round_id,omitempty"`
	RunID        *uuid.UUID `json:"run_id,omitempty"`
	RepositoryID uuid.UUID  `json:"repository_id"`
	Kind         string     `json:"kind"`
}

// Admit waits until the scheduler admits the work and returns the
// admission ID (§7.3). It heartbeats while it waits; a waiter or holder
// that stops heartbeating loses its place.
func (a *Activities) Admit(ctx context.Context, in AdmitInput) (uuid.UUID, error) {
	prio := 1
	if in.Kind != KindRound {
		prio = 0
	}
	var id uuid.UUID
	err := a.Pool.QueryRow(ctx, `SELECT id FROM admissions WHERE released_at IS NULL AND (round_id = $1 OR run_id = $2)`, in.RoundID, in.RunID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		id = uuid.Must(uuid.NewV7())
		_, err = a.Pool.Exec(ctx, `INSERT INTO admissions (id, round_id, run_id, repository_id, kind, priority) VALUES ($1, $2, $3, $4, $5, $6)`,
			id, in.RoundID, in.RunID, in.RepositoryID, in.Kind, prio)
	}
	if err != nil {
		return uuid.Nil, err
	}
	poll := 500 * time.Millisecond
	for {
		admitted, err := a.tryAdmit(ctx, id)
		if err != nil {
			return uuid.Nil, err
		}
		if admitted {
			return id, nil
		}
		if activity.IsActivity(ctx) {
			activity.RecordHeartbeat(ctx, id.String())
		}
		select {
		case <-ctx.Done():
			_, _ = a.Pool.Exec(context.WithoutCancel(ctx), `UPDATE admissions SET released_at = now() WHERE id = $1`, id)
			return uuid.Nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}

// tryAdmit admits the next waiter when a slot is free and reports whether
// that waiter is id. Order: reply and fix first, oldest first; then rounds
// round-robin across repositories (the repository admitted least recently
// goes first), oldest first in a repository, within max_concurrent_rounds.
func (a *Activities) tryAdmit(ctx context.Context, id uuid.UUID) (bool, error) {
	limit := a.intSetting(ctx, settings.WorkerMaxAgentRuns, 4)
	repoDefault := a.intSetting(ctx, settings.MaxConcurrentRounds, 2)
	var admitted bool
	err := pgx.BeginFunc(ctx, a.Pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, admissionLock); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE admissions SET heartbeat_at = now() WHERE id = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE admissions SET released_at = now() WHERE released_at IS NULL AND heartbeat_at < now() - $1::interval`, staleAfter.String()); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT admitted_at IS NOT NULL FROM admissions WHERE id = $1`, id).Scan(&admitted); err != nil || admitted {
			return err
		}
		var running int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM admissions WHERE admitted_at IS NOT NULL AND released_at IS NULL`).Scan(&running); err != nil {
			return err
		}
		if running >= limit {
			return nil
		}
		var next uuid.UUID
		err := tx.QueryRow(ctx, `
			WITH waiting AS (
				SELECT w.id, w.repository_id, w.kind, w.priority, w.enqueued_at FROM admissions w
				WHERE w.admitted_at IS NULL AND w.released_at IS NULL
			), busy AS (
				SELECT repository_id, count(*) AS n FROM admissions
				WHERE admitted_at IS NOT NULL AND released_at IS NULL AND kind = 'round' GROUP BY repository_id
			), last AS (
				SELECT repository_id, max(admitted_at) AS at FROM admissions WHERE admitted_at IS NOT NULL AND kind = 'round' GROUP BY repository_id
			)
			SELECT w.id FROM waiting w
			JOIN repositories r ON r.id = w.repository_id
			LEFT JOIN busy b ON b.repository_id = w.repository_id
			LEFT JOIN last l ON l.repository_id = w.repository_id
			WHERE w.kind <> 'round' OR coalesce(b.n, 0) < coalesce(r.max_concurrent_rounds, $1)
			ORDER BY w.priority, CASE WHEN w.kind = 'round' THEN coalesce(l.at, '-infinity') END NULLS FIRST, w.enqueued_at
			LIMIT 1`, repoDefault).Scan(&next)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE admissions SET admitted_at = now() WHERE id = $1`, next); err != nil {
			return err
		}
		admitted = next == id
		return nil
	})
	return admitted, err
}

// Release frees the slot of an admission.
func (a *Activities) Release(ctx context.Context, id uuid.UUID) error {
	_, err := a.Pool.Exec(ctx, `UPDATE admissions SET released_at = now() WHERE id = $1 AND released_at IS NULL`, id)
	return err
}

// HoldAdmission keeps a held slot alive while the agent run works.
func (a *Activities) HoldAdmission(ctx context.Context, id uuid.UUID) error {
	_, err := a.Pool.Exec(ctx, `UPDATE admissions SET heartbeat_at = now() WHERE id = $1`, id)
	return err
}
