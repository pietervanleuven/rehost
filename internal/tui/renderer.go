// Package tui renders rehost output in three modes — styled for terminals,
// plain for non-TTY (CI, pipes), JSON for machines — and provides the
// interactive prompters the ssh layer needs. tui imports ssh, never the
// reverse.
package tui

import (
	"io"

	"github.com/placeholder/rehost/internal/detect"
	"github.com/placeholder/rehost/internal/ssh"
)

// Mode selects the output format.
type Mode int

const (
	ModeStyled Mode = iota
	ModePlain
	ModeJSON
)

// HostReport pairs a host's role with what was found on it: probed
// capabilities and any detected framework installs.
type HostReport struct {
	Role     string // "source" or "destination"
	Caps     *ssh.Capabilities
	Installs []detect.Install
}

// Renderer writes rehost reports in one output mode.
type Renderer interface {
	CapabilityReport(reports []HostReport) error
	Error(err error)
}

// New returns the renderer for a mode, writing to out.
func New(mode Mode, out io.Writer) Renderer {
	switch mode {
	case ModePlain:
		return plainRenderer{out: out}
	case ModeJSON:
		return jsonRenderer{out: out}
	default:
		return styledRenderer{out: out}
	}
}
