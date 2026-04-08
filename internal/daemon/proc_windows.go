//go:build windows

package daemon

import (
	"fmt"
	"time"
)

// findChildPID is not yet implemented on Windows.
func findChildPID(parentPID int, timeout time.Duration) (int, error) {
	return 0, fmt.Errorf("--project mode is not yet supported on Windows")
}
