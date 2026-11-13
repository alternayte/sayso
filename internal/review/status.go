package review

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/sayso/internal/settings"
)

// StatusBody is the state that every page shows: the budget banner (§7.3).
type StatusBody struct {
	BudgetSpent      bool    `json:"budget_spent" doc:"The monthly budget is spent; no new round starts"`
	MonthlyBudgetUSD float64 `json:"monthly_budget_usd"`
	MonthSpentUSD    float64 `json:"month_spent_usd"`
}

// MonthlySpend returns the cost of this month and the monthly budget.
func MonthlySpend(ctx context.Context, pool *pgxpool.Pool, st *settings.Store) (spent, budget float64, err error) {
	if err = st.Decode(ctx, settings.MonthlyBudgetUSD, &budget); err != nil {
		return 0, 0, err
	}
	err = pool.QueryRow(ctx, `SELECT coalesce(sum(cost_usd), 0) FROM agent_runs WHERE created_at >= date_trunc('month', now())`).Scan(&spent)
	return spent, budget, err
}

// RegisterStatus adds GET /status for signed-in people.
func RegisterStatus(api huma.API, pool *pgxpool.Pool, st *settings.Store, view map[string]any) {
	huma.Register(api, huma.Operation{OperationID: "get-status", Method: http.MethodGet, Path: "/status", Tags: []string{"status"}, Metadata: view},
		func(ctx context.Context, _ *struct{}) (*struct{ Body StatusBody }, error) {
			spent, budget, err := MonthlySpend(ctx, pool, st)
			if err != nil {
				return nil, err
			}
			return &struct{ Body StatusBody }{StatusBody{BudgetSpent: budget > 0 && spent >= budget, MonthlyBudgetUSD: budget, MonthSpentUSD: spent}}, nil
		})
}
