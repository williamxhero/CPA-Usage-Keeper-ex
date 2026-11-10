package test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// Windows system bash is a WSL launcher and does not preserve the native
// checkout or environment. Use Git for Windows bash for these local scripts.
func releaseBash(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		if git, err := exec.LookPath("git"); err == nil {
			shell := filepath.Join(filepath.Dir(git), "..", "bin", "bash.exe")
			if _, err := os.Stat(shell); err == nil {
				return shell
			}
		}
		t.Fatal("Git for Windows bash is required for native release script checks")
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is required for release script checks")
	}
	return shell
}
