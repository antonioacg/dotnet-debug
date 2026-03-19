package paths

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

var nonAlphanumeric = regexp.MustCompile(`[^a-z0-9]+`)

// BaseDir returns ~/.dotnet-debug (cross-platform).
func BaseDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		if runtime.GOOS == "windows" {
			home = os.Getenv("USERPROFILE")
		} else {
			home = os.Getenv("HOME")
		}
	}
	return filepath.Join(home, ".dotnet-debug")
}

// SessionsDir returns the directory where session files are stored.
func SessionsDir() string {
	return filepath.Join(BaseDir(), "sessions")
}

// SessionFile returns the path for a specific session's JSON file.
func SessionFile(id string) string {
	return filepath.Join(SessionsDir(), id+".json")
}

// LogFile returns the path for a session's log file.
func LogFile(id string) string {
	return filepath.Join(BaseDir(), "logs", id+".log")
}

// EnsureDirs creates the base directory structure.
func EnsureDirs() error {
	dirs := []string{
		SessionsDir(),
		filepath.Join(BaseDir(), "logs"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0700); err != nil {
			return fmt.Errorf("creating directory %s: %w", d, err)
		}
	}
	return nil
}

// GenerateSessionID creates an ID from the DLL name + next sequence number.
// Example: "MyApp.dll" with no existing sessions -> "myapp-1"
// With "myapp-1" existing -> "myapp-2"
func GenerateSessionID(dllPath string) string {
	base := filepath.Base(dllPath)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	base = strings.ToLower(base)
	base = nonAlphanumeric.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "debug"
	}

	// Find next available sequence number
	entries, _ := os.ReadDir(SessionsDir())
	maxSeq := 0
	prefix := base + "-"
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if strings.HasPrefix(name, prefix) {
			suffix := strings.TrimPrefix(name, prefix)
			if n, err := strconv.Atoi(suffix); err == nil && n > maxSeq {
				maxSeq = n
			}
		}
	}
	return fmt.Sprintf("%s-%d", base, maxSeq+1)
}

// ListSessionFiles returns all session file paths, sorted by modification time (newest first).
func ListSessionFiles() ([]string, error) {
	entries, err := os.ReadDir(SessionsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	type fileWithTime struct {
		path    string
		modTime int64
	}
	var files []fileWithTime
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			p := filepath.Join(SessionsDir(), e.Name())
			info, err := e.Info()
			if err == nil {
				files = append(files, fileWithTime{path: p, modTime: info.ModTime().UnixNano()})
			}
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].modTime > files[j].modTime })

	paths := make([]string, len(files))
	for i, f := range files {
		paths[i] = f.path
	}
	return paths, nil
}

// FindNetcoredbg locates netcoredbg, checking (in order):
// 1. NETCOREDBG_PATH env var
// 2. ~/.dotnet-debug/bin/ (our managed install, preferred over system)
// 3. PATH lookup
// 4. Platform-specific default locations
func FindNetcoredbg() string {
	if p := os.Getenv("NETCOREDBG_PATH"); p != "" {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	binaryName := "netcoredbg"
	if runtime.GOOS == "windows" {
		binaryName = "netcoredbg.exe"
	}

	// Check our managed install first (preferred — known good binary)
	managed := []string{
		filepath.Join(BaseDir(), "bin", "netcoredbg", binaryName), // Cliffback tarball layout
		filepath.Join(BaseDir(), "bin", binaryName),
	}
	for _, p := range managed {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}

	// Fall back to PATH
	if p, err := exec.LookPath("netcoredbg"); err == nil {
		return p
	}

	candidates := []string{}

	switch runtime.GOOS {
	case "darwin", "linux":
		candidates = append(candidates,
			filepath.Join("/usr/local/bin", binaryName),
			filepath.Join("/usr/bin", binaryName),
		)
	case "windows":
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			candidates = append(candidates, filepath.Join(pf, "netcoredbg", binaryName))
		}
		if localAppData := os.Getenv("LOCALAPPDATA"); localAppData != "" {
			candidates = append(candidates, filepath.Join(localAppData, "netcoredbg", binaryName))
		}
	}

	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}
