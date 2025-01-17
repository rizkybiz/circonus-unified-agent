// +build !windows

package execd

import (
	"fmt"
	"io"
	"os"
	"syscall"
	"time"

	"github.com/circonus-labs/circonus-unified-agent/cua"
)

func (e *Execd) Gather(acc cua.Accumulator) error {
	if e.process == nil || e.process.Cmd == nil {
		return nil
	}

	osProcess := e.process.Cmd.Process
	if osProcess == nil {
		return nil
	}
	switch e.Signal {
	case "SIGHUP":
		_ = osProcess.Signal(syscall.SIGHUP)
	case "SIGUSR1":
		_ = osProcess.Signal(syscall.SIGUSR1)
	case "SIGUSR2":
		_ = osProcess.Signal(syscall.SIGUSR2)
	case "STDIN":
		if osStdin, ok := e.process.Stdin.(*os.File); ok {
			_ = osStdin.SetWriteDeadline(time.Now().Add(1 * time.Second))
		}
		if _, err := io.WriteString(e.process.Stdin, "\n"); err != nil {
			return fmt.Errorf("Error writing to stdin: %w", err)
		}
	case "none":
	default:
		return fmt.Errorf("invalid signal: %s", e.Signal)
	}

	return nil
}
