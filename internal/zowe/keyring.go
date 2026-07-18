package zowe

import "errors"

// Keyring is the narrow part of an operating-system credential vault that
// Zowe configuration loading needs. Keeping this interface in cq makes the
// Zowe parser independently testable and leaves room for a different vault
// implementation without coupling profile resolution to it.
type Keyring interface {
	Get(service, account string) (string, error)
}

// ErrSecretNotFound is returned by Keyring implementations for a missing entry.
var ErrSecretNotFound = errors.New("secret not found in keyring")

type systemKeyring struct{}
