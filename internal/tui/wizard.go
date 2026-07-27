package tui

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"charm.land/huh/v2"

	"github.com/placeholder/rehost/internal/project"
)

// ErrAborted is returned when the user cancels a wizard form (esc / ctrl+c),
// so callers can distinguish "changed their mind" from a real failure without
// importing huh.
var ErrAborted = errors.New("aborted")

func wrapAborted(err error) error {
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrAborted
	}
	return err
}

// ProjectNameForm asks for the project name; *name is the pre-filled default
// and receives the answer.
func ProjectNameForm(name *string) error {
	return wrapAborted(huh.NewInput().
		Title("Project name").
		Description("Identifies this migration in reports and history.").
		Validate(requireNonEmpty("a project name")).
		Value(name).
		Run())
}

// HostForm collects the connection details for one host. h is both the
// pre-fill (so a retry keeps earlier answers) and the result. It never asks
// for a secret: passwords are prompted at connect time, never stored.
func HostForm(role string, h *project.Host) error {
	label := strings.ToUpper(role[:1]) + role[1:]
	port := ""
	if h.Port != 0 {
		port = strconv.Itoa(h.Port)
	}
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title(label+" host").
				Description("Hostname, IP address, or a Host alias from ~/.ssh/config.").
				Validate(validateHost).
				Value(&h.Host),
			huh.NewInput().
				Title("SSH user").
				Description("Leave empty to use ~/.ssh/config or your local username.").
				Value(&h.User),
			huh.NewInput().
				Title("SSH port").
				Placeholder("22").
				Description("Leave empty for ~/.ssh/config or 22.").
				Validate(validatePort).
				Value(&port),
			huh.NewSelect[string]().
				Title("Authentication").
				Description("Passwords are prompted at connect time and never stored.").
				Options(
					huh.NewOption("auto — agent, then default keys, then password prompt", ""),
					huh.NewOption("SSH agent", "agent"),
					huh.NewOption("key file", "key"),
					huh.NewOption("password (prompted, never stored)", "password"),
				).
				Value(&h.Auth),
		),
		huh.NewGroup(
			huh.NewInput().
				Title("Key file path").
				Placeholder("~/.ssh/id_ed25519").
				Description("Leave empty to use the IdentityFile from ~/.ssh/config.").
				Value(&h.KeyPath),
		).WithHideFunc(func() bool { return h.Auth != "key" }),
	)
	if err := wrapAborted(form.Run()); err != nil {
		return err
	}
	h.Port = 0
	if port != "" {
		h.Port, _ = strconv.Atoi(port) // validated above
	}
	if h.Auth != "key" {
		h.KeyPath = ""
	}
	return nil
}

// ConfirmAction asks a yes/no question with a default answer.
func ConfirmAction(title, description string, def bool) (bool, error) {
	ok := def
	err := wrapAborted(huh.NewConfirm().
		Title(title).
		Description(description).
		Value(&ok).
		Run())
	return ok, err
}

// RetryChoice is the user's decision after a failed connection test.
type RetryChoice string

const (
	RetryEdit  RetryChoice = "edit"
	RetrySave  RetryChoice = "save"
	RetryAbort RetryChoice = "abort"
)

// ConnectFailedChoice presents the recovery options after a connection test
// failed with connErr.
func ConnectFailedChoice(role string, connErr error) (RetryChoice, error) {
	choice := RetryEdit
	err := wrapAborted(huh.NewSelect[RetryChoice]().
		Title(fmt.Sprintf("Could not connect to the %s host", role)).
		Description(connErr.Error()).
		Options(
			huh.NewOption("edit the connection details and retry", RetryEdit),
			huh.NewOption("keep these details anyway and verify later with 'rehost plan'", RetrySave),
			huh.NewOption("abort without saving", RetryAbort),
		).
		Value(&choice).
		Run())
	return choice, err
}

func requireNonEmpty(what string) func(string) error {
	return func(s string) error {
		if strings.TrimSpace(s) == "" {
			return fmt.Errorf("enter %s", what)
		}
		return nil
	}
}

func validateHost(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("enter a host")
	}
	if strings.Contains(s, "@") {
		return errors.New("enter only the host here; the user has its own field")
	}
	if strings.ContainsAny(s, " \t") {
		return errors.New("a host cannot contain spaces")
	}
	return nil
}

func validatePort(s string) error {
	if s == "" {
		return nil
	}
	p, err := strconv.Atoi(s)
	if err != nil || p < 1 || p > 65535 {
		return errors.New("enter a port between 1 and 65535, or leave empty")
	}
	return nil
}
