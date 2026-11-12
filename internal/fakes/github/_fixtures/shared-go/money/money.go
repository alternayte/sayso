// Package money has shared money helpers.
package money

// Cents converts whole units to cents.
func Cents(units int64) int64 { return units * 100 }
