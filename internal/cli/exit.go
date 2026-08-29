package cli

import "errors"

// GateError marks a deliberate refusal — compatibility blockers or the
// destination-state policy — as opposed to an operational failure (transport
// errors, bad flags). main maps it to exit code 2 so scripts can tell "fix
// the environment and rerun" apart from "something broke".
type GateError struct{ Err error }

func (g GateError) Error() string { return g.Err.Error() }
func (g GateError) Unwrap() error { return g.Err }

// Exit codes: 0 converged/green, 1 operational failure, 2 gate refusal.
const (
	ExitFailure = 1
	ExitBlocked = 2
)

// ExitCode maps a returned error to the process exit code.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var gate GateError
	if errors.As(err, &gate) {
		return ExitBlocked
	}
	return ExitFailure
}
