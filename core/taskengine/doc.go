/*
Task Engine handles task storage and execution. We use badgerdb for all of our task storage. We like to make sure of Go cross compiling extensively and want to leverage pure-go as much as possible. badgerdb sastify that requirement.

**Wallet Info**

w:<eoa>:<smart-wallet-address> = {factory_address: address, salt: salt}

**Task Storage**

w:<eoa>:<smart-wallet-address> -> {factory, salt}
t:<task-status>:<task-id>      -> task payload, the source of truth of task information
u:<eoa>:<smart-wallet-address>:<task-id>    -> task status
history:<task-id>:<execution-id> -> an execution history

The task storage was designed for fast retrieve time at the cost of extra storage.

The storage can also be easily back-up, sync due to simplicity of supported write operation.

**Data console**

Storage can also be inspect with telnet:

	telnet /tmp/ap.sock

Then issue `get <ket>` or `list <prefix>` or `list *` to inspect current keys in the storage.
*/
package taskengine
