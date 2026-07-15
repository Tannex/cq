//go:build !linux && !freebsd && !netbsd && !openbsd && !dragonfly && !windows && !darwin

package main

import (
	"errors"

	keyring "github.com/zalando/go-keyring"
)

func (systemZoweKeyring) Get(service, account string) (string, error) {
	value, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", errZoweSecretNotFound
	}
	return value, err
}
