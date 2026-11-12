// Package api serves the payment endpoints.
package api

import (
	"fmt"

	"github.com/acme/payments/ledger"
)

// Charge posts a charge of cents to the account ledger.
func Charge(l *ledger.Ledger, cents int64) (string, error) {
	if err := l.Post(cents); err != nil {
		return "", fmt.Errorf("charge: %w", err)
	}
	return fmt.Sprintf("charged %d", cents), nil
}
