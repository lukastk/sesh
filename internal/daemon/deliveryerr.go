package daemon

// The TYPED answer to the one question a delivery failure must settle: could the
// message have reached the agent?
//
// It exists because the browser send has to publish an explicit outcome
// (`refused_dead` vs `uncertain`), and the first implementation decided that by
// matching the daemon's own error TEXT ("nothing pasted", "delivery may be
// uncertain"). That coupling is a silent-reclassification bug waiting to happen:
// rewording a message a human reads would quietly change a machine-readable
// outcome, and PI_DELIVERY's rule — never retry an uncertain submission — hangs
// off that outcome.
//
// Two classifications, and the asymmetry is deliberate:
//
//   - deliveryRefused: NOTHING was delivered. Every site that returns one has
//     positive evidence of that (no live pane, no socket, a failed dial, a
//     socket that belongs to another pane, a guard read that happened before any
//     write).
//   - deliveryUncertain: the write may or may not have been submitted. The pi
//     RPC message whose acknowledgement did not arrive is the canonical case.
//
// An UNCLASSIFIED error is treated as uncertain, never as a refusal. Claiming
// "nothing happened" without evidence is the dangerous direction: it invites the
// caller to resend a message the agent may already be answering.
//
// Both wrappers delegate Error() to the wrapped error, so the text every CLI and
// log line already shows is unchanged; only the classification moved out of the
// text.

// deliveryRefused marks a failure that provably delivered nothing.
type deliveryRefused struct{ err error }

func (e deliveryRefused) Error() string { return e.err.Error() }
func (e deliveryRefused) Unwrap() error { return e.err }

// deliveryUncertain marks a failure after which submission is unknown. Do not
// retry one automatically; see _dev/PI_DELIVERY.md.
type deliveryUncertain struct{ err error }

func (e deliveryUncertain) Error() string { return e.err.Error() }
func (e deliveryUncertain) Unwrap() error { return e.err }
