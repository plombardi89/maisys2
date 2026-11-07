// Package windows holds the Windows integration test, which CI runs on a
// hosted windows-2025 runner with real WSL 2: maisys.exe syncs a Windows
// host from the end-to-end tests' fixture, creating WSL distros and setting
// them up with the Linux maisys inside. See wsl_test.go.
package windows
