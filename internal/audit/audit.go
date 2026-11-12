// Package audit writes and reads the append-only audit log. The database
// refuses UPDATE, DELETE and TRUNCATE on the table (SI-13).
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// Actor is who did an action.
type Actor struct {
	UserID *uuid.UUID
	Kind   string // user, system or github
	Login  string
}

// System is the actor for actions that Sayso takes by itself.
var System = Actor{Kind: "system", Login: "system"}

// User returns the actor for a signed-in person.
func User(id uuid.UUID, login string) Actor {
	return Actor{UserID: &id, Kind: "user", Login: login}
}

// Write appends one entry. Pass a transaction to make the entry part of the
// change it records.
func Write(ctx context.Context, db DBTX, a Actor, action, target string, details map[string]any) error {
	if details == nil {
		details = map[string]any{}
	}
	raw, err := json.Marshal(details)
	if err != nil {
		return fmt.Errorf("audit details: %w", err)
	}
	return New(db).Insert(ctx, InsertParams{
		ActorUserID: a.UserID, ActorKind: a.Kind, ActorLogin: a.Login,
		Action: action, Target: target, Details: raw,
	})
}

// Query filters the log. A zero value lists the newest entries.
type Query struct {
	Action   string
	BeforeID int64
	Limit    int32
}

// Search lists entries newest first.
func Search(ctx context.Context, db DBTX, q Query) ([]AuditLog, error) {
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	p := ListParams{Limit: q.Limit}
	if q.Action != "" {
		p.Action = &q.Action
	}
	if q.BeforeID > 0 {
		p.BeforeID = &q.BeforeID
	}
	return New(db).List(ctx, p)
}
