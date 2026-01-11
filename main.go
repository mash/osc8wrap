package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/creack/pty"
	"golang.org/x/term"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "Usage: %s <command> [args...]\n", os.Args[0])
		os.Exit(1)
	}

	hostname, _ := os.Hostname()
	cwd, _ := os.Getwd()

	cmd := exec.Command(os.Args[1], os.Args[2:]...)

	ptmx, err := pty.Start(cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start pty: %v\n", err)
		os.Exit(1)
	}
	defer ptmx.Close()

	handleResize(ptmx)
	forwardSignals(cmd)

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err == nil {
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	linker := NewLinker(os.Stdout, cwd, hostname)

	go io.Copy(ptmx, os.Stdin)

	io.Copy(linker, ptmx)

	linker.Flush()

	cmd.Wait()

	exitCode := 0
	if cmd.ProcessState != nil {
		exitCode = cmd.ProcessState.ExitCode()
	}
	os.Exit(exitCode)
}

func handleResize(ptmx *os.File) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGWINCH)

	go func() {
		for range ch {
			pty.InheritSize(os.Stdin, ptmx)
		}
	}()

	ch <- syscall.SIGWINCH
}

func forwardSignals(cmd *exec.Cmd) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		for sig := range ch {
			if cmd.Process != nil {
				cmd.Process.Signal(sig)
			}
		}
	}()
}
