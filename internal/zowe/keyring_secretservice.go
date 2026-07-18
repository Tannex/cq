//go:build linux || freebsd || netbsd || openbsd || dragonfly

package zowe

import (
	dbus "github.com/godbus/dbus/v5"
	ss "github.com/zalando/go-keyring/secret_service"
)

// Get reads the Secret Service attributes written by Zowe's libsecret
// backend. They are named "service" and "account"; the generic go-keyring
// API uses "username" instead, so using it directly would not find an
// existing Zowe entry on Linux/BSD.
func (systemKeyring) Get(service, account string) (string, error) {
	vault, err := ss.NewSecretService()
	if err != nil {
		return "", err
	}
	// Zowe stores into libsecret's COLLECTION_DEFAULT alias. Do not prefer a
	// separately named "login" collection when the user's default points
	// somewhere else.
	collection := vault.Object("org.freedesktop.secrets", dbus.ObjectPath("/org/freedesktop/secrets/aliases/default"))
	if err := vault.Unlock(collection.Path()); err != nil {
		return "", err
	}
	items, err := vault.SearchItems(collection, map[string]string{
		"service": service,
		"account": account,
	})
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return "", ErrSecretNotFound
	}
	return getZoweSecretServiceItem(vault, items[0])
}

func getZoweSecretServiceItem(vault *ss.SecretService, item dbus.ObjectPath) (string, error) {
	session, err := vault.OpenSession()
	if err != nil {
		return "", err
	}
	defer vault.Close(session)
	if err := vault.Unlock(item); err != nil {
		return "", err
	}
	secret, err := vault.GetSecret(item, session.Path())
	if err != nil {
		return "", err
	}
	return string(secret.Value), nil
}
