package tui

import (
	"errors"
	"fmt"
	"os"

	"charm.land/huh/v2"
)

// HuhPrompter answers ssh.Prompter interactively on a TTY. Prompts render on
// stderr: they are conversation, not document output, so a piped or
// redirected stdout (plain mode, `| tee`) stays clean while the operator
// still gets asked.
type HuhPrompter struct{}

// runField runs one field as a form pinned to the terminal streams.
func runField(f huh.Field) error {
	return huh.NewForm(huh.NewGroup(f)).WithInput(os.Stdin).WithOutput(os.Stderr).Run()
}

func (HuhPrompter) Password(prompt string) (string, error) {
	var pw string
	input := huh.NewInput().
		Title(prompt).
		EchoMode(huh.EchoModePassword).
		Value(&pw)
	if err := runField(input); err != nil {
		return "", err
	}
	return pw, nil
}

func (HuhPrompter) ConfirmHostKey(host, keyType, fingerprint string) (bool, error) {
	var ok bool
	confirm := huh.NewConfirm().
		Title(fmt.Sprintf("Unknown host %s", host)).
		Description(fmt.Sprintf("Host key: %s %s\nTrust this host and add it to ~/.ssh/known_hosts?", keyType, fingerprint)).
		Value(&ok)
	if err := runField(confirm); err != nil {
		return false, err
	}
	return ok, nil
}

// NonInteractivePrompter fails every prompt with remediation guidance; it is
// used for --json and non-TTY runs so nothing ever blocks a pipe.
type NonInteractivePrompter struct{}

func (NonInteractivePrompter) Password(string) (string, error) {
	return "", errors.New("a password or passphrase is required but no interactive terminal is available — use ssh-agent or an unencrypted key, or run rehost in a terminal")
}

func (NonInteractivePrompter) ConfirmHostKey(host, keyType, fingerprint string) (bool, error) {
	return false, fmt.Errorf("host %s is not in known_hosts (%s %s) and cannot be confirmed non-interactively — connect once with `ssh %s` or run rehost in a terminal", host, keyType, fingerprint, host)
}
