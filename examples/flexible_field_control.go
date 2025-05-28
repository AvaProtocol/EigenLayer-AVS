package examples

import (
	"time"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

// SecretFieldMask defines which fields should be included in the response
type SecretFieldMask struct {
	Name        bool
	Scope       bool
	WorkflowId  bool
	OrgId       bool
	CreatedAt   bool
	UpdatedAt   bool
	CreatedBy   bool
	Description bool
}

// DefaultSecretFieldMask returns the default fields for listing secrets
func DefaultSecretFieldMask() SecretFieldMask {
	return SecretFieldMask{
		Name:       true,
		Scope:      true,
		WorkflowId: true,
		OrgId:      true,
		// By default, don't include metadata fields for performance
		CreatedAt:   false,
		UpdatedAt:   false,
		CreatedBy:   false,
		Description: false,
	}
}

// DetailedSecretFieldMask returns all fields for detailed secret views
func DetailedSecretFieldMask() SecretFieldMask {
	return SecretFieldMask{
		Name:        true,
		Scope:       true,
		WorkflowId:  true,
		OrgId:       true,
		CreatedAt:   true,
		UpdatedAt:   true,
		CreatedBy:   true,
		Description: true,
	}
}

// SecretData represents the full secret data from storage
type SecretData struct {
	Name        string
	Scope       string
	WorkflowId  string
	OrgId       string
	CreatedAt   int64
	UpdatedAt   int64
	CreatedBy   string
	Description string
}

// PopulateSecretWithMask populates a Secret protobuf message based on the field mask
func PopulateSecretWithMask(data *SecretData, mask SecretFieldMask) *avsproto.Secret {
	secret := &avsproto.Secret{}

	if mask.Name {
		secret.Name = data.Name
	}
	if mask.Scope {
		secret.Scope = data.Scope
	}
	if mask.WorkflowId {
		secret.WorkflowId = data.WorkflowId
	}
	if mask.OrgId {
		secret.OrgId = data.OrgId
	}
	if mask.CreatedAt {
		secret.CreatedAt = data.CreatedAt
	}
	if mask.UpdatedAt {
		secret.UpdatedAt = data.UpdatedAt
	}
	if mask.CreatedBy {
		secret.CreatedBy = data.CreatedBy
	}
	if mask.Description {
		secret.Description = data.Description
	}

	return secret
}

// Example usage in service layer:

// ListSecretsWithFieldControl demonstrates how to use field control in the service
func ListSecretsWithFieldControl(includeMetadata bool) []*avsproto.Secret {
	// Sample data from storage
	secretsData := []*SecretData{
		{
			Name:        "api_key",
			Scope:       "user",
			WorkflowId:  "workflow_123",
			OrgId:       "org_456",
			CreatedAt:   time.Now().Unix(),
			UpdatedAt:   time.Now().Unix(),
			CreatedBy:   "user_789",
			Description: "API key for external service",
		},
		{
			Name:        "db_password",
			Scope:       "workflow",
			WorkflowId:  "workflow_456",
			OrgId:       "",
			CreatedAt:   time.Now().Unix(),
			UpdatedAt:   time.Now().Unix(),
			CreatedBy:   "user_789",
			Description: "Database password for workflow",
		},
	}

	// Choose field mask based on requirements
	var mask SecretFieldMask
	if includeMetadata {
		mask = DetailedSecretFieldMask()
	} else {
		mask = DefaultSecretFieldMask()
	}

	// Populate secrets with selective fields
	var secrets []*avsproto.Secret
	for _, data := range secretsData {
		secret := PopulateSecretWithMask(data, mask)
		secrets = append(secrets, secret)
	}

	return secrets
}

// Advanced field control with query parameters
type ListSecretsOptions struct {
	IncludeTimestamps  bool
	IncludeCreatedBy   bool
	IncludeDescription bool
}

// CreateFieldMaskFromOptions creates a field mask from query options
func CreateFieldMaskFromOptions(opts ListSecretsOptions) SecretFieldMask {
	mask := DefaultSecretFieldMask()

	if opts.IncludeTimestamps {
		mask.CreatedAt = true
		mask.UpdatedAt = true
	}
	if opts.IncludeCreatedBy {
		mask.CreatedBy = true
	}
	if opts.IncludeDescription {
		mask.Description = true
	}

	return mask
}

// Example HTTP handler pattern:
/*
func (h *Handler) ListSecrets(w http.ResponseWriter, r *http.Request) {
	// Parse query parameters
	opts := ListSecretsOptions{
		IncludeTimestamps:  r.URL.Query().Get("include_timestamps") == "true",
		IncludeCreatedBy:   r.URL.Query().Get("include_created_by") == "true",
		IncludeDescription: r.URL.Query().Get("include_description") == "true",
	}

	// Create field mask
	mask := CreateFieldMaskFromOptions(opts)

	// Fetch and populate secrets
	secretsData := h.fetchSecretsFromStorage()
	var secrets []*avsproto.Secret
	for _, data := range secretsData {
		secret := PopulateSecretWithMask(data, mask)
		secrets = append(secrets, secret)
	}

	// Return response
	response := &avsproto.ListSecretsResp{
		Items: secrets,
		PageInfo: &avsproto.PageInfo{...},
	}

	// Serialize and send response
}
*/

// Performance considerations:
// 1. Only fetch required fields from storage when possible
// 2. Use field masks to avoid expensive operations (e.g., timestamp formatting)
// 3. Cache frequently accessed metadata separately
// 4. Consider using different storage queries for different field requirements

// Security considerations:
// 1. Always validate field access permissions before populating
// 2. Some fields might require additional authorization
// 3. Audit which fields are being requested by whom
// 4. Rate limit requests that include expensive fields
