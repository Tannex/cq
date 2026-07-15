package main

import "errors"

// zoweKeyring is the narrow part of an operating-system credential vault that
// Zowe configuration loading needs. Keeping this interface in cq makes the
// Zowe parser independently testable and leaves room for a different vault
// implementation without coupling profile resolution to it.
type zoweKeyring interface {
	Get(service, account string) (string, error)
}

var errZoweSecretNotFound = errors.New("secret not found in keyring")

type systemZoweKeyring struct{}
