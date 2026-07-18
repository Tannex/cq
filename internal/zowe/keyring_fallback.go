//go:build !linux && !freebsd && !netbsd && !openbsd && !dragonfly && !windows

package zowe

import (
	"errors"

	keyring "github.com/zalando/go-keyring"
)

func (systemKeyring) Get(service, account string) (string, error) {
	value, err := keyring.Get(service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrSecretNotFound
	}
	return value, err
}
