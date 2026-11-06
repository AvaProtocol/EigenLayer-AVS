package taskengine

// BudgetSubmitError describes this submit request's send boundary. False is
// returned only before any first-send marker was attempted under the wallet
// locks. True is deliberately conservative, including a failed marker write.
// Neither an unknown error nor a lost HTTP reply proves that nothing was sent.
type BudgetSubmitError struct {
	Cause             error
	SubmissionStarted bool
}

func (e *BudgetSubmitError) Error() string { return e.Cause.Error() }
func (e *BudgetSubmitError) Unwrap() error { return e.Cause }
