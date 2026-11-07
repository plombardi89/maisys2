// Package signin holds the private config repo sign-in test, which CI runs
// on pushes to main: maisys clones a private copy of the end-to-end tests'
// fixture over HTTPS, signing in with MAISYS_CONFIG_TOKEN, and with a token
// typed at its prompt. See signin_test.go.
package signin
