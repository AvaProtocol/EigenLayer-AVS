package taskengine

const (
	TaskNotFoundError = "task not found"

	InvalidSmartAccountAddressError = "invalid smart account address"
	InvalidFactoryAddressError      = "invalid factory address"
	InvalidSmartAccountSaltError    = "invalid salt value"
	SmartAccountCreationError       = "cannot determine smart wallet address"
	NonceFetchingError              = "cannot determine nonce for smart wallet"

	StorageUnavailableError = "storage is not ready"
	StorageWriteError       = "cannot write to storage"

	TaskStorageCorruptedError = "task data storage is corrupted"
)
