//go:build windows

package main

import (
	"errors"
	"syscall"

	"github.com/danieljoos/wincred"
)

// Get uses Zowe's service/account target name. Generic Go keyring packages
// commonly use service:account, which names a different Windows credential.
func (systemZoweKeyring) Get(service, account string) (string, error) {
	credential, err := wincred.GetGenericCredential(service + "/" + account)
	if errors.Is(err, syscall.ERROR_NOT_FOUND) {
		return "", errZoweSecretNotFound
	}
	if err != nil {
		return "", err
	}
	return string(credential.CredentialBlob), nil
}
