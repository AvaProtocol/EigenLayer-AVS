package auth

const (
	AuthenticationError      = "User authentication error"
	InvalidSignatureFormat   = "Unable to decode client's signature. Please check the message's format."
	InvalidAuthenticationKey = "User Auth key is invalid"
	InvalidAPIKey            = "API key is invalid"
	MalformedExpirationTime  = "Malformed Expired Time"
)
