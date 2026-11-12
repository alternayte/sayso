// Package ledger records payments.
package ledger

import "errors"

// ErrNegative is returned for a negative amount.
var ErrNegative = errors.New("ledger: negative amount")

// Ledger holds the balance of one account in cents.
type Ledger struct {
	balance int64
}

// Post adds amount cents to the balance.
func (l *Ledger) Post(amount int64) error {
	if amount < 0 {
		return ErrNegative
	}
	l.balance += amount
	return nil
}

// Balance returns the balance in cents.
func (l *Ledger) Balance() int64 { return l.balance }
