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
	TaskIsNotRunable          = "The workflow is not in a runable status, it has reached the limit execution or the expiration time"

	InvalidCursor          = "cursor is not valid"
	InvalidPaginationParam = "item per page is not valid"
	DefaultItemPerPage     = 50

	InvalidEntrypoint = "cannot detect task entrypoint from trigger and edge data"
)
