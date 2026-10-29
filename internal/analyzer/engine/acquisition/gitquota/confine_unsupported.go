//go:build !linux || !amd64

package gitquota

import "fmt"

func DropPrivileges() error                                   { return fmt.Errorf("hard Git confinement currently requires Linux amd64") }
func NamespaceArgs(string, string, string, []string) []string { return nil }
func CheckNamespace(string) error {
	return fmt.Errorf("hard Git confinement currently requires Linux amd64")
}

func ConfinementABI() (int, error) {
	return 0, fmt.Errorf("hard Git confinement currently requires Linux amd64")
}
func Confine(string) error { return fmt.Errorf("hard Git confinement currently requires Linux amd64") }
