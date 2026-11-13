package findings

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/alternayte/sayso/internal/runner"
)

// Finding is one row of the ledger.
type Finding struct {
	ID           uuid.UUID
	ReviewID     uuid.UUID
	Seq          int
	Severity     string
	Category     string
	Title        string
	Body         string
	Path         string
	StartLine    int
	EndLine      int
	CommitSHA    string
	Evidence     runner.Evidence
	Suggestion   string
	Status       string
	Blocking     bool
	Challenge    *runner.Challenge
	PreviousID   *uuid.UUID
	ThreadNodeID string
	CommentID    *int64
	FirstRoundID uuid.UUID
	LastRoundID  uuid.UUID
}

// Label is the ID that people see: S-<seq>.
func (f Finding) Label() string { return "S-" + strconv.Itoa(f.Seq) }

const columns = `id, review_id, seq, severity, category, title, body, path, start_line, end_line, commit_sha, evidence, suggestion, status,
	blocking, challenge, previous_id, thread_node_id, comment_id, first_round_id, last_round_id`

func scan(row pgx.Row) (Finding, error) {
	var f Finding
	var ev, ch []byte
	err := row.Scan(&f.ID, &f.ReviewID, &f.Seq, &f.Severity, &f.Category, &f.Title, &f.Body, &f.Path, &f.StartLine, &f.EndLine, &f.CommitSHA,
		&ev, &f.Suggestion, &f.Status, &f.Blocking, &ch, &f.PreviousID, &f.ThreadNodeID, &f.CommentID, &f.FirstRoundID, &f.LastRoundID)
	if err != nil {
		return f, err
	}
	_ = json.Unmarshal(ev, &f.Evidence)
	if len(ch) > 0 && string(ch) != "null" {
		f.Challenge = &runner.Challenge{}
		_ = json.Unmarshal(ch, f.Challenge)
	}
	return f, nil
}

// List returns the findings of a review in seq order.
func List(ctx context.Context, q pgx.Tx, reviewID uuid.UUID) ([]Finding, error) {
	rows, err := q.Query(ctx, `SELECT `+columns+` FROM findings WHERE review_id = $1 ORDER BY seq`, reviewID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Finding
	for rows.Next() {
		f, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// Event writes one finding_events row (§9.4).
func Event(ctx context.Context, tx pgx.Tx, findingID uuid.UUID, roundID *uuid.UUID, actor, typ, reason string) error {
	_, err := tx.Exec(ctx, `INSERT INTO finding_events (id, finding_id, round_id, actor, type, reason) VALUES ($1, $2, $3, $4, $5, $6)`,
		uuid.Must(uuid.NewV7()), findingID, roundID, actor, typ, reason)
	return err
}

// SetStatus changes the status of a finding and writes the event.
func SetStatus(ctx context.Context, tx pgx.Tx, id uuid.UUID, roundID *uuid.UUID, status, event, actor, reason string) error {
	if _, err := tx.Exec(ctx, `UPDATE findings SET status = $2, blocking = CASE WHEN $2 = 'open' THEN blocking ELSE false END, updated_at = now() WHERE id = $1`, id, status); err != nil {
		return err
	}
	return Event(ctx, tx, id, roundID, actor, event, reason)
}

// Input is what ReconcileLedger needs from a round.
type Input struct {
	ReviewID     uuid.UUID
	RepositoryID uuid.UUID
	RoundID      uuid.UUID
	HeadSHA      string
	Result       runner.Result
	// Block is the overlay value: false means findings never block.
	Block bool
	// Threshold is the title similarity for a repeat (default 0.6).
	Threshold float64
}

// Reopened is a dismissed finding that the round reports again with new
// test evidence.
type Reopened struct {
	FindingID uuid.UUID
	Evidence  runner.Evidence
}

// Outcome tells Publish what the round changed.
type Outcome struct {
	// New are the new open findings of the round, to post.
	New []uuid.UUID
	// Fixed are findings that the round fixed: reply and resolve the thread.
	Fixed []uuid.UUID
	// Reopened get one reply in their old thread.
	Reopened []Reopened
	// Withdrawn and Suppressed are for the summary.
	Withdrawn  []uuid.UUID
	Suppressed []uuid.UUID
	Verdict    string
}

const agent = "agent"

// Reconcile applies the result of a round to the ledger (§9).
func Reconcile(ctx context.Context, tx pgx.Tx, in Input) (Outcome, error) {
	var out Outcome
	if in.Threshold == 0 {
		in.Threshold = 0.6
	}
	round := &in.RoundID
	ledger, err := List(ctx, tx, in.ReviewID)
	if err != nil {
		return out, err
	}
	bySeq := map[string]*Finding{}
	byID := map[uuid.UUID]*Finding{}
	maxSeq := 0
	for i := range ledger {
		f := &ledger[i]
		bySeq[f.Label()] = f
		byID[f.ID] = f
		maxSeq = max(maxSeq, f.Seq)
	}

	// Ledger updates of findings that were open.
	for _, u := range in.Result.LedgerUpdates {
		f := bySeq[u.FindingID]
		if f == nil || f.Status != StatusOpen {
			continue
		}
		switch u.Status {
		case "still_open":
			if u.Path != "" {
				f.Path, f.StartLine, f.EndLine = u.Path, u.StartLine, u.EndLine
			}
			if _, err := tx.Exec(ctx, `UPDATE findings SET path = $2, start_line = $3, end_line = $4, last_round_id = $5, commit_sha = $6, updated_at = now() WHERE id = $1`,
				f.ID, f.Path, f.StartLine, f.EndLine, in.RoundID, in.HeadSHA); err != nil {
				return out, err
			}
			if err := Event(ctx, tx, f.ID, round, agent, "still_open", u.Reason); err != nil {
				return out, err
			}
		case StatusFixed:
			if err := SetStatus(ctx, tx, f.ID, round, StatusFixed, "fixed", agent, u.Reason); err != nil {
				return out, err
			}
			f.Status, f.Blocking = StatusFixed, false
			out.Fixed = append(out.Fixed, f.ID)
		case StatusWithdrawn:
			if err := SetStatus(ctx, tx, f.ID, round, StatusWithdrawn, "withdrawn", agent, u.Reason); err != nil {
				return out, err
			}
			f.Status, f.Blocking = StatusWithdrawn, false
			out.Withdrawn = append(out.Withdrawn, f.ID)
		}
	}

	insert := func(c runner.Finding, status string, blocking bool, previous *uuid.UUID) (*Finding, error) {
		maxSeq++
		f := Finding{ID: uuid.Must(uuid.NewV7()), ReviewID: in.ReviewID, Seq: maxSeq, Severity: c.Severity, Category: c.Category, Title: c.Title, Body: c.Body,
			Path: NormalizePath(c.Path), StartLine: c.StartLine, EndLine: c.EndLine, CommitSHA: in.HeadSHA, Evidence: c.Evidence, Suggestion: c.Suggestion,
			Status: status, Blocking: blocking, Challenge: c.Challenge, PreviousID: previous, FirstRoundID: in.RoundID, LastRoundID: in.RoundID}
		ev, _ := json.Marshal(f.Evidence)
		var ch []byte
		if f.Challenge != nil {
			ch, _ = json.Marshal(f.Challenge)
		}
		_, err := tx.Exec(ctx, `INSERT INTO findings (id, review_id, fingerprint, seq, severity, category, title, body, path, start_line, end_line,
			commit_sha, evidence, suggestion, status, blocking, challenge, previous_id, first_round_id, last_round_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $19)`,
			f.ID, f.ReviewID, Fingerprint(in.RepositoryID, f.Category, f.Path, f.Title), f.Seq, f.Severity, f.Category, f.Title, f.Body, f.Path,
			f.StartLine, f.EndLine, f.CommitSHA, ev, f.Suggestion, f.Status, f.Blocking, ch, f.PreviousID, in.RoundID)
		if err != nil {
			return nil, err
		}
		if err := Event(ctx, tx, f.ID, round, agent, "raised", ""); err != nil {
			return nil, err
		}
		ledger = append(ledger, f)
		p := &ledger[len(ledger)-1]
		byID[f.ID], bySeq[f.Label()] = p, p
		return p, nil
	}

	for _, c := range in.Result.Findings {
		// A refuted candidate, and a downgraded nit, is stored withdrawn and
		// not posted; a downgrade drops one severity level.
		sev, withdrawn := ApplyChallenge(c.Severity, c.Challenge)
		if withdrawn {
			f, err := insert(c, StatusWithdrawn, false, nil)
			if err != nil {
				return out, err
			}
			if err := Event(ctx, tx, f.ID, round, agent, "withdrawn", "challenge "+c.Challenge.Outcome+": "+c.Challenge.Reason); err != nil {
				return out, err
			}
			out.Withdrawn = append(out.Withdrawn, f.ID)
			continue
		}
		c.Severity = sev
		blocking := Blocking(c.Severity, c.Evidence.Kind, c.Challenge, in.Block)
		target := match(ledger, c, in.Threshold)
		if target == nil {
			f, err := insert(c, StatusOpen, blocking, nil)
			if err != nil {
				return out, err
			}
			out.New = append(out.New, f.ID)
			continue
		}
		switch target.Status {
		case StatusOpen:
			// A repeat of an open finding updates it; no new comment.
			target.Path, target.StartLine, target.EndLine = NormalizePath(c.Path), c.StartLine, c.EndLine
			target.Severity, target.Blocking = c.Severity, blocking
			ev, _ := json.Marshal(c.Evidence)
			if _, err := tx.Exec(ctx, `UPDATE findings SET path = $2, start_line = $3, end_line = $4, last_round_id = $5, commit_sha = $6,
				severity = $7, blocking = $8, evidence = $9, updated_at = now() WHERE id = $1`,
				target.ID, target.Path, target.StartLine, target.EndLine, in.RoundID, in.HeadSHA, c.Severity, blocking, ev); err != nil {
				return out, err
			}
			if err := Event(ctx, tx, target.ID, round, agent, "reraised", ""); err != nil {
				return out, err
			}
		case StatusDismissed, StatusConceded, StatusResolvedByHuman:
			if target.Status == StatusResolvedByHuman {
				if _, err := tx.Exec(ctx, `INSERT INTO steering_signals (id, review_id, round_id, finding_id, type, detail) VALUES ($1, $2, $3, $4, 'resolved_unfixed', $5)`,
					uuid.Must(uuid.NewV7()), in.ReviewID, in.RoundID, target.ID, fmt.Sprintf(`{"finding":%q}`, target.Label())); err != nil {
					return out, err
				}
			}
			if c.Evidence.Kind == "test" && target.Evidence.Kind != "test" {
				// New test evidence reopens it; it does not block in this round.
				ev, _ := json.Marshal(c.Evidence)
				if _, err := tx.Exec(ctx, `UPDATE findings SET status = 'open', blocking = false, evidence = $2, last_round_id = $3, commit_sha = $4,
					path = $5, start_line = $6, end_line = $7, updated_at = now() WHERE id = $1`,
					target.ID, ev, in.RoundID, in.HeadSHA, NormalizePath(c.Path), c.StartLine, c.EndLine); err != nil {
					return out, err
				}
				if err := Event(ctx, tx, target.ID, round, agent, "reopened", "new test evidence: "+c.Evidence.TestPath); err != nil {
					return out, err
				}
				target.Status, target.Blocking, target.Evidence = StatusOpen, false, c.Evidence
				out.Reopened = append(out.Reopened, Reopened{FindingID: target.ID, Evidence: c.Evidence})
				continue
			}
			if err := Event(ctx, tx, target.ID, round, agent, "suppressed", "repeat of a "+target.Status+" finding"); err != nil {
				return out, err
			}
			out.Suppressed = append(out.Suppressed, target.ID)
		case StatusFixed:
			prev := target.ID
			f, err := insert(c, StatusOpen, blocking, &prev)
			if err != nil {
				return out, err
			}
			out.New = append(out.New, f.ID)
		default: // withdrawn: a new finding
			f, err := insert(c, StatusOpen, blocking, nil)
			if err != nil {
				return out, err
			}
			out.New = append(out.New, f.ID)
		}
	}

	var open []Finding
	for _, f := range ledger {
		if f.Status == StatusOpen {
			open = append(open, f)
		}
	}
	out.Verdict = Verdict(open)
	return out, nil
}

// match finds the ledger entry that a candidate repeats (§9.3): same_as
// first, then the same path and category with similar titles.
func match(ledger []Finding, c runner.Finding, threshold float64) *Finding {
	if c.SameAs != "" {
		for i := range ledger {
			if strings.EqualFold(ledger[i].Label(), c.SameAs) {
				return &ledger[i]
			}
		}
	}
	title, path, cat := NormalizeTitle(c.Title), NormalizePath(c.Path), strings.ToLower(c.Category)
	var best *Finding
	bestScore := 0.0
	for i := range ledger {
		f := &ledger[i]
		if f.Path != path || strings.ToLower(f.Category) != cat {
			continue
		}
		if s := Similarity(NormalizeTitle(f.Title), title); s >= threshold && s >= bestScore {
			best, bestScore = f, s
		}
	}
	return best
}
