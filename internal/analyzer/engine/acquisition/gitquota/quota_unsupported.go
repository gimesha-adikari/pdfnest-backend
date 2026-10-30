//go:build !linux

package gitquota

import "errors"

var ErrUnavailable = errors.New("hard Git quotas require Linux amd64")

type Manager struct{}
type Lease struct{}

func Load(string) (*Manager, error)       { return nil, ErrUnavailable }
func (*Manager) Probe() error             { return ErrUnavailable }
func (*Manager) Acquire() (*Lease, error) { return nil, ErrUnavailable }
func (*Lease) Path() string               { return "" }
func (*Lease) Helper() string             { return "" }
func (*Lease) ConfigPath() string         { return "" }
func (*Lease) Release() error             { return ErrUnavailable }

func (*Lease) Token() string { return "" }
