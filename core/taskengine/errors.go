package taskengine

const (
	InternalError          = "internal error"
	TaskNotFoundError      = "task not found"
	ExecutionNotFoundError = "execution not found"

	InvalidSmartAccountAddressError = "invalid smart account address"
	InvalidFactoryAddressError      = "invalid factory address"
	InvalidSmartAccountSaltError    = "invalid salt value"
	InvalidTaskIdFormat             = "invalid task id"
	SmartAccountCreationError       = "cannot determine smart wallet address"
	NonceFetchingError              = "cannot determine nonce for smart wallet"

	MissingSmartWalletAddressError = "Missing smart_wallet_address"

	StorageUnavailableError      = "storage is not ready"
	StorageWriteError            = "cannot write to storage"
	StorageQueueUnavailableError = "queue storage system is not ready"

	TaskStorageCorruptedError = "task data storage is corrupted"
	TaskIDMissing             = "Missing task id in request"
	TaskIsNotRunnable         = "task cannot be executed: either reached max execution, expired, or not yet started"

	InvalidCursor          = "cursor is not valid"
	InvalidPaginationParam = "item per page is not valid"

	InvalidEntrypoint = "cannot detect task entrypoint from trigger and edge data"
)
