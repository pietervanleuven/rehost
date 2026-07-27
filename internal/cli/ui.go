package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/placeholder/rehost/internal/ssh"
	"github.com/placeholder/rehost/internal/tui"
)

// ui bundles the per-command presentation state every host-touching command
// needs: the resolved output mode and everything that follows from it.
type ui struct {
	mode        tui.Mode
	renderer    tui.Renderer
	interactive bool // prompts (passwords, TOFU, forms) allowed
	prompter    ssh.Prompter
	progress    func(format string, a ...any)
}

// newUI resolves the output mode once and derives the rest: prompts only
// exist on an interactive terminal, progress goes to stderr so the report on
// stdout stays clean, and JSON mode stays silent so nothing but the document
// reaches a consumer.
func newUI(cmd *cobra.Command, opts *options) ui {
	mode := opts.outputMode()
	u := ui{
		mode:        mode,
		renderer:    tui.New(mode, cmd.OutOrStdout()),
		interactive: mode == tui.ModeStyled,
	}
	if u.interactive {
		u.prompter = tui.HuhPrompter{}
	} else {
		u.prompter = tui.NonInteractivePrompter{}
	}
	u.progress = func(format string, a ...any) {
		if mode != tui.ModeJSON {
			fmt.Fprintf(cmd.ErrOrStderr(), format+"\n", a...)
		}
	}
	return u
}

// outputMode picks the renderer: --json wins, then plain for non-TTY or
// suppressed color, styled otherwise.
func (o *options) outputMode() tui.Mode {
	if o.json {
		return tui.ModeJSON
	}
	if o.noColor || os.Getenv("NO_COLOR") != "" {
		return tui.ModePlain
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return tui.ModePlain
	}
	return tui.ModeStyled
}
