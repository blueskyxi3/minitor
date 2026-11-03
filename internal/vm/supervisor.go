package vm

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// Supervise is an internal self-exec mode. The inherited pipe closes even when
// the plugin is SIGKILLed, so the database is reaped on both macOS and Linux.
func Supervise(args []string) error {
	if len(args) == 0 {
		return errors.New("database executable is required")
	}
	parent := os.NewFile(3, "monitor-parent")
	if parent == nil {
		return errors.New("missing parent supervision pipe")
	}
	defer parent.Close()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	closed := make(chan struct{})
	go func() { _, _ = io.Copy(io.Discard, parent); close(closed) }()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case err := <-done:
		return err
	case <-closed:
	case <-signals:
	}
	if err := cmd.Process.Signal(os.Interrupt); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	select {
	case <-done:
		return nil
	case <-time.After(8 * time.Second):
		if err := cmd.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			return err
		}
		<-done
		return nil
	}
}
