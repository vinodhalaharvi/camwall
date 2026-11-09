//go:build !linux

package feeds

import "os/exec"

// Child is a no-op where the system has no parent-death signal; camwall stops
// its FFmpeg processes itself when it shuts down.
func Child(cmd *exec.Cmd) *exec.Cmd { return cmd }
