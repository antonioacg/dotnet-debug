//go:build !windows

package daemon

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// findChildPID polls for a child process of the given parent PID.
// Used in project mode to find the .NET app spawned by `dotnet run`.
func findChildPID(parentPID int, timeout time.Duration) (int, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := exec.Command("pgrep", "-P", strconv.Itoa(parentPID)).Output()
		if err == nil {
			lines := strings.Split(strings.TrimSpace(string(out)), "\n")
			for _, line := range lines {
				line = strings.TrimSpace(line)
				if pid, err := strconv.Atoi(line); err == nil && pid > 0 {
					return pid, nil
				}
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return 0, fmt.Errorf("no child process found for PID %d after %v", parentPID, timeout)
}
