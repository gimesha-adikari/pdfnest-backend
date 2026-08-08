package process

import "errors"

var (
	ErrProcessStart     = errors.New("process failed to start")
	ErrProcessTimeout   = errors.New("process execution timed out")
	ErrProcessCancelled = errors.New("process execution cancelled")
	ErrProcessExit      = errors.New("process exited with non-zero status")
	ErrProcessGroupKill = errors.New("failed to kill process group")
)
