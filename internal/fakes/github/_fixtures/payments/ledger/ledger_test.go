package ledger

import "testing"

func TestPost(t *testing.T) {
	var l Ledger
	if err := l.Post(250); err != nil {
		t.Fatal(err)
	}
	if l.Balance() != 250 {
		t.Fatalf("balance %d", l.Balance())
	}
	if err := l.Post(-1); err != ErrNegative {
		t.Fatalf("negative amount: %v", err)
	}
}
