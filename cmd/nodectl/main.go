// Command nodectl starts, stops and probes the lab node process in a way that
// works the same on Windows (Git Bash) and Unix.
//
//	nodectl start -log node.log -pid node.pid -- geth --dev ...   start detached, write the pid
//	nodectl stop  <pid> [seconds]                                 graceful stop (Ctrl-C equivalent)
//	nodectl alive <pid>                                           exit 0 if the process exists
//
// Why this exists: on Windows a bash script cannot send Ctrl-C to a background
// process (kill -INT does nothing for native processes). The trick used here:
// start the node with its OWN hidden console, then attach to that console and
// broadcast a Ctrl-Break event, which Go programs (geth, labnode) treat as
// os.Interrupt. Because the console is private, nothing else is interrupted.
// Hard kill is not here: `taskkill /F` or `kill -9` already do it.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "start":
		start(os.Args[2:])
	case "stop":
		if len(os.Args) < 3 {
			usage()
		}
		pid, _ := strconv.Atoi(os.Args[2])
		timeout := 20
		if len(os.Args) > 3 {
			timeout, _ = strconv.Atoi(os.Args[3])
		}
		if !alive(pid) {
			fmt.Println("not running")
			return
		}
		if err := interrupt(pid); err != nil {
			fmt.Fprintln(os.Stderr, "nodectl: interrupt:", err)
			os.Exit(1)
		}
		deadline := time.Now().Add(time.Duration(timeout) * time.Second)
		for time.Now().Before(deadline) {
			if !alive(pid) {
				fmt.Println("stopped")
				return
			}
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Fprintln(os.Stderr, "nodectl: still running after timeout")
		os.Exit(3)
	case "alive":
		if len(os.Args) < 3 {
			usage()
		}
		pid, _ := strconv.Atoi(os.Args[2])
		if alive(pid) {
			return
		}
		os.Exit(1)
	default:
		usage()
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: nodectl start -log F -pid F -- cmd args... | stop PID [secs] | alive PID")
	os.Exit(2)
}

func start(args []string) {
	fs := flag.NewFlagSet("start", flag.ExitOnError)
	logPath := fs.String("log", "node.log", "append stdout+stderr here")
	pidPath := fs.String("pid", "node.pid", "write the pid here")
	_ = fs.Parse(args)
	rest := fs.Args()
	if len(rest) == 0 {
		usage()
	}
	lf, err := os.OpenFile(*logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "nodectl:", err)
		os.Exit(1)
	}
	cmd := exec.Command(rest[0], rest[1:]...)
	cmd.Stdout, cmd.Stderr = lf, lf
	detach(cmd)
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "nodectl:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*pidPath, []byte(strconv.Itoa(cmd.Process.Pid)), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "nodectl:", err)
		os.Exit(1)
	}
	fmt.Println(cmd.Process.Pid)
}
