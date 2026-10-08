//go:build !linux

package repo

import "fmt"

// Reconciliation fails closed where an atomic no-replace directory move has
// not been implemented. Official Tugboat binaries currently target Linux.
func renameNoReplace(from, to string) error {
	return fmt.Errorf("atomic no-replace checkout moves are unsupported on this platform")
}

func lockCheckout(dir string) (func(), error) {
	return nil, fmt.Errorf("checkout reconciliation locks are unsupported on this platform")
}
