package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/placeholder/rehost/internal/project"
	"github.com/placeholder/rehost/internal/ssh"
	"github.com/placeholder/rehost/internal/tui"
)

func newInitCmd(opts *options) *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Interactive wizard: connection details for both hosts, connectivity test, project file",
		Long: `init walks through configuring the source and destination hosts, tests
that each one connects, and writes the project file (migrate.yaml).

Secrets are never stored: migrate.yaml has no field that can hold a password;
rehost prompts at connect time instead. The wizard needs an interactive
terminal — in scripts, write the project file by hand.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runInit(cmd, opts, force)
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing project file without asking")
	return cmd
}

func runInit(cmd *cobra.Command, opts *options, force bool) error {
	if opts.outputMode() != tui.ModeStyled {
		fmt.Fprintf(cmd.ErrOrStderr(), "Write %s by hand instead:\n\n%s\n", opts.projectFile, project.Example())
		return errors.New("rehost init is an interactive wizard and needs a terminal — it cannot run with --json or piped/suppressed output")
	}
	out := cmd.OutOrStdout()

	if _, err := os.Stat(opts.projectFile); err == nil && !force {
		overwrite, err := tui.ConfirmAction(
			fmt.Sprintf("%s already exists", opts.projectFile),
			"Overwrite it? The current contents will be lost.", false)
		if err != nil {
			return initErr(err)
		}
		if !overwrite {
			fmt.Fprintf(out, "Keeping %s untouched.\n", opts.projectFile)
			return nil
		}
	}

	f := &project.File{Version: project.SchemaVersion, Name: defaultProjectName()}
	if err := tui.ProjectForm(&f.Name, &f.Domain); err != nil {
		return initErr(err)
	}

	if err := collectHost(cmd, "source", &f.Source); err != nil {
		return initErr(err)
	}

	wantDest, err := tui.ConfirmAction("Configure the destination host now?",
		"'rehost check' and 'rehost migrate' need it; you can also add it to migrate.yaml later.", true)
	if err != nil {
		return initErr(err)
	}
	if wantDest {
		var dest project.Host
		if err := collectHost(cmd, "destination", &dest); err != nil {
			return initErr(err)
		}
		f.Destination = &dest
	}

	if err := f.Save(opts.projectFile); err != nil {
		return err
	}
	fmt.Fprintf(out, "\nWrote %s.\n\nNext: rehost plan — probe the hosts and detect the websites on them.\n", opts.projectFile)
	return nil
}

// collectHost runs the host form and connection test in a loop until the host
// connects, the user keeps unverified details, or the user aborts.
func collectHost(cmd *cobra.Command, role string, h *project.Host) error {
	errOut := cmd.ErrOrStderr()
	for {
		if err := tui.HostForm(role, h); err != nil {
			return err
		}
		fmt.Fprintf(errOut, "%s: connecting to %s…\n", role, targetLabel(h.SSHConfig()))
		caps, err := testConnection(cmd.Context(), h.SSHConfig())
		if err == nil {
			fmt.Fprintf(errOut, "%s: connected to %s (%s)\n", role, caps.Target(), caps.Summary())
			return nil
		}
		choice, cerr := tui.ConnectFailedChoice(role, err)
		if cerr != nil {
			return cerr
		}
		switch choice {
		case tui.RetryEdit:
			continue
		case tui.RetrySave:
			return nil
		default:
			return tui.ErrAborted
		}
	}
}

// testConnection dials and probes one host. Password and host-key prompts
// come from the interactive prompter — init only runs on a TTY.
func testConnection(ctx context.Context, cfg ssh.Config) (*ssh.Capabilities, error) {
	client, err := ssh.Dial(ctx, cfg, tui.HuhPrompter{})
	if err != nil {
		return nil, err
	}
	defer client.Close()
	return ssh.Probe(ctx, client)
}

// initErr turns a wizard cancellation into a clean exit message; any other
// error passes through.
func initErr(err error) error {
	if errors.Is(err, tui.ErrAborted) {
		return errors.New("init cancelled — nothing was written")
	}
	return err
}

// defaultProjectName suggests the working directory name, matching what a
// user checking migrate.yaml into that directory would call the project.
func defaultProjectName() string {
	wd, err := os.Getwd()
	if err != nil {
		return ""
	}
	return filepath.Base(wd)
}
