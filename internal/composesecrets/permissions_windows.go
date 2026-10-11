//go:build windows

package composesecrets

import "errors"

// The server deploys on Linux. Until a Windows service-account ACL implementation
// is provided, secret materialization on Windows fails closed instead of relying
// on inherited ACLs or pretending chmod protects private files.
func protectDirectory(string) error {
	return errors.New("protected secret directories are unsupported on Windows")
}
func protectFile(string) error {
	return errors.New("protected secret files are unsupported on Windows")
}
func validatePrivateDirectory(string) error {
	return errors.New("protected secret directories are unsupported on Windows")
}
func validateIdentityFile(string) error {
	return errors.New("protected secret files are unsupported on Windows")
}
func recipientFromIdentityFile(string) (string, error) {
	return "", errors.New("protected secret files are unsupported on Windows")
}
