//go:build darwin

package main

import (
	"errors"

	keyring "github.com/zalando/go-keyring"
)

// macOS generic-password entries natively expose the same service/account
// pair used by Zowe.
func (systemZoweKeyring) Get(service, account string) (string, error) {
	value, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errZoweSecretNotFound
	}
	return value, err
}
