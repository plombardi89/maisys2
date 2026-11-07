package system

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeEnv makes the test binary act as another program instead of running
// the tests: "helper" (see helper) or "sudo" (see fakeSudo).
const fakeEnv = "SYSTEM_TEST_FAKE"

func TestMain(m *testing.M) {
	switch os.Getenv(fakeEnv) {
	case "helper":
		os.Exit(helper(os.Args[1:]))
	case "sudo":
		os.Exit(fakeSudo(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// testBinary returns the path of the running test binary.
func testBinary(t *testing.T) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return exe
}

// helperCmd returns a Cmd that runs the test binary as a helper with args.
func helperCmd(t *testing.T, args ...string) Cmd {
	t.Helper()
	return Cmd{Name: testBinary(t), Args: args, Env: []string{fakeEnv + "=helper"}}
}

// helper acts out args:
//   - exit CODE STDOUT STDERR writes STDOUT and STDERR, then exits with CODE
//   - env NAME prints the environment variable NAME
//   - pwd prints the working directory
//   - cat copies its input to its output
//   - sleep sleeps for a minute
//   - group prints "own" if it runs in a process group of its own, and
//     "parent" if it runs in its parent's (see processGroup)
func helper(args []string) int {
	switch args[0] {
	case "exit":
		fmt.Print(args[2])
		fmt.Fprint(os.Stderr, args[3])
		code, err := strconv.Atoi(args[1])
		if err != nil {
			return 99
		}
		return code
	case "env":
		fmt.Println(os.Getenv(args[1]))
	case "pwd":
		wd, err := os.Getwd()
		if err != nil {
			return 99
		}
		fmt.Println(wd)
	case "cat":
		if _, err := io.Copy(os.Stdout, os.Stdin); err != nil {
			return 99
		}
	case "sleep":
		time.Sleep(time.Minute)
	case "group":
		fmt.Println(processGroup())
	default:
		return 99
	}
	return 0
}

// fakeSudo acts as sudo, the way the FAKE_SUDO_MODE environment variable
// says, and appends its arguments as a JSON array to the file named by
// FAKE_SUDO_LOG. If FAKE_SUDO_GROUPS names a file, it also appends its
// arguments and process group (see processGroup) there, as a line of
// "ARGS: GROUP". The modes are:
//   - nopasswd: sudo works without a password
//   - password: sudo needs the password "secret", unless the file named by
//     FAKE_SUDO_STAMP exists; reading the password creates it
//   - rule: sudo runs commands without a password, through a NOPASSWD
//     rule, but sudo -v asks for one, because of the user's group's rule
//   - notallowed: the user may not use sudo
//   - broken: sudo fails as it does on a nosuid file system
func fakeSudo(args []string) int {
	b, err := json.Marshal(args)
	if err != nil {
		return 99
	}
	if f, err := os.OpenFile(os.Getenv("FAKE_SUDO_LOG"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600); err == nil {
		_, _ = f.Write(append(b, '\n'))
		_ = f.Close()
	}
	if name := os.Getenv("FAKE_SUDO_GROUPS"); name != "" {
		if f, err := os.OpenFile(name, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600); err == nil {
			_, _ = fmt.Fprintf(f, "%s: %s\n", strings.Join(args, " "), processGroup())
			_ = f.Close()
		}
	}

	var nonInteractive, validate, list bool
	i := 0
options:
	for ; i < len(args); i++ {
		switch args[i] {
		case "-n":
			nonInteractive = true
		case "-v":
			validate = true
		case "-l":
			list = true
		case "--":
			i++
			break options
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintf(os.Stderr, "sudo: unknown option %s\n", args[i])
				return 1
			}
			break options
		}
	}
	readPassword := func() string {
		line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		return strings.TrimSpace(line)
	}

	switch os.Getenv("FAKE_SUDO_MODE") {
	case "broken":
		fmt.Fprintln(os.Stderr, "sudo: effective uid is not 0, is /usr/bin/sudo on a file system with the 'nosuid' option set or an NFS file system without root privileges?")
		return 1
	case "notallowed":
		switch {
		case nonInteractive && list:
			fmt.Fprintln(os.Stderr, "Sorry, user tester may not run sudo on testhost.")
		case nonInteractive:
			fmt.Fprintln(os.Stderr, "sudo: a password is required")
		default:
			readPassword()
			fmt.Fprintln(os.Stderr, "tester is not in the sudoers file.")
		}
		return 1
	case "password":
		stamp := os.Getenv("FAKE_SUDO_STAMP")
		if _, err := os.Stat(stamp); err != nil {
			if nonInteractive {
				fmt.Fprintln(os.Stderr, "sudo: a password is required")
				return 1
			}
			if readPassword() != "secret" {
				fmt.Fprintln(os.Stderr, "sudo: 1 incorrect password attempt")
				return 1
			}
			if err := os.WriteFile(stamp, nil, 0o600); err != nil {
				return 99
			}
		}
	case "rule":
		// A NOPASSWD rule for the user, besides their group's rule, which
		// needs a password: commands run without one, but sudo -v asks for
		// it, as it does unless every rule is NOPASSWD.
		if validate && nonInteractive {
			fmt.Fprintln(os.Stderr, "sudo: a password is required")
			return 1
		}
		if validate {
			readPassword()
			fmt.Fprintln(os.Stderr, "sudo: 1 incorrect password attempt")
			return 1
		}
	}
	if validate || list {
		return 0
	}
	if i >= len(args) {
		fmt.Fprintln(os.Stderr, "usage: sudo command")
		return 1
	}

	cmd := exec.CommandContext(context.Background(), args[i], args[i+1:]...)
	cmd.Env = slices.DeleteFunc(os.Environ(), func(kv string) bool { return strings.HasPrefix(kv, fakeEnv+"=") })
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	err = cmd.Run()
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		return exitErr.ExitCode()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "sudo: %s: command not found\n", args[i])
		return 1
	}
	return 0
}
