# Workflow write confirmation

Dependent writes require successful write ancestors. Reads, code, reports and
independent branches continue after failures. This applies to simulation as well
as deployed execution; simulation never looks up or submits a real UserOperation.

Before skipping a dependent write for a pending prerequisite, the VM checks each
stored UserOperation receipt once, with a five-second deadline for the entire
prerequisite. It never resubmits the prerequisite or starts another poll loop.
A confirmed success updates the original execution step and permits the next
write. A confirmed revert records USER_OP_EXECUTION_REVERTED (4007) and skips the
write. Pending, unavailable or malformed receipts remain unresolved (4006) and
skip the write (4008). Check the operation status before starting another run.
This check also runs when execution resumes after Await.

Loop collector timeouts retain a snapshot of a submitted iteration, including
its pending operation hash, on metadata.confirmationOutcomes. A resumed dependent
write can reconcile these snapshots. Resolving every pending iteration only clears
the loop failure when no unrelated iteration/collector failures remain. Loop
iterations keep their existing sequencing and do not become atomic groups.

Confirmation uses the runner node's chain configuration and EntryPoint, including
for ephemeral loop runners. A gateway checkpoint with no identifiable chain is
left unresolved rather than checked against an arbitrary default chain.

For contract-write receipts, status is the outer Ethereum transaction status.
An inner UserOperation revert therefore normally retains status 0x1; its receipt
executionStatus is failed, the method and step success are false, and the step
errorCode is 4007. An outer transaction revert has status 0x0. Only the matching
UserOperationRevertReason event from the resolved chain's EntryPoint is decoded.

Skipped writes have metadata.skipped=true and no transaction or gas. Their
output.data is an empty object, and node input/data variables exist for terminal
reports. The failed prerequisite retains its receipt, hash and actual effects.
