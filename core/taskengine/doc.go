/*
Task Engine handles task storage and execution. We use badgerdb for all of our task storage. We like to make sure of Go cross compiling extensively and want to leverage pure-go as much as possible. badgerdb sastify that requirement.

In KV store, we want to use short key to save space. The key is what loaded into RAM, the smaller the better. It's also helpful for key only scan.

**Wallet Info**

w:<eoa>:<smart-wallet-address> = {factory_address: address, salt: salt}

**Task Storage**

w:<eoa>:<smart-wallet-address> -> {factory, salt}
t:<task-status>:<task-id>      -> task payload, the source of truth of task information
u:<eoa>:<smart-wallet-address>:<task-id>    -> task status
history:<task-id>:<execution-id> -> an execution history
trigger:<task-id>:<execution-id> -> execution status
ct:cw:<eoa> -> counter value -> track contract write by eoa

The task storage was designed for fast retrieve time at the cost of extra storage.

The storage can also be easily back-up, sync due to simplicity of supported write operation.

**Data console**

Storage can also be inspect with telnet:

	telnet /tmp/ap.sock

Then issue `get <ket>` or `list <prefix>` or `list *` to inspect current keys in the storage.

**Secret Storage**
- currently org_id will always be _ because we haven't implemented it yet
- workflow_id will also be _ so we can search be prefix
secret:<org_id>:<eoa>:<workflow_id>:<name> -> value
*/
package taskengine
