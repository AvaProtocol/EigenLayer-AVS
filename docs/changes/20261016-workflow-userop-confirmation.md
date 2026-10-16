# Resolve delayed UserOperations before reporting their outcome

When the initial bundler send returns a pending operation, workflow execution
polls for its receipt before continuing. That polling path previously treated
any non-null receipt as success. A mined EntryPoint transaction can contain a
reverted UserOperation, so this could turn a failed approval into a successful
step. The waiter also read transaction hashes only from the receipt's top
level and retained pending method metadata after confirmation.

The waiter now uses the same receipt parser as the UserOperation status API.
It checks the operation's `success`, reads the standard nested transaction
receipt, and updates the matching method's success, error, transaction hash
and execution status. A multi-call node resolves each pending operation and
computes its overall outcome from all methods. ETH transfers use the same
operation outcome and replace their pending transaction link.

If confirmation cannot be resolved, receipt metadata remains pending. The
workflow step records an unresolved-confirmation error, asking the caller to
check status before retrying. This means the workflow could not establish its
outcome; it does not claim that the on-chain operation reverted. Keeping a
nonempty error prevents the execution analyzer from declaring a successful
workflow while a transaction is still unresolved.

Sequential DAG execution still releases downstream nodes after predecessor
completion, including failures. This change establishes truthful confirmation
results and reporting; it does not add rollback, atomic grouping, or global
failure-based cancellation. Execution mode remains owned by the workflow,
and the removed per-node `isSimulated` setting stays removed.

Regression tests use local HTTP RPC fixtures, with no on-chain writes. They
cover confirmed success, inner reverts despite successful outer transactions,
unresolved confirmation, nested transaction hashes, ETH transfers and
multiple pending operations in one node.
