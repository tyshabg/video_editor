//go:build windows

package ffmpeg

import (
	"context"
	"os/exec"
	"syscall"
)

// command builds an exec.Cmd whose console window stays hidden. Without this
// every ffmpeg/ffprobe child of the GUI app pops up its own console.
func command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000, // CREATE_NO_WINDOW
	}
	return cmd
}
