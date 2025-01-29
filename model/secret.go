package model

type Secret struct {
	Name  string `validate:"required"`
	Value string `validate:"required"`

	// User is the original EOA that create the secret
	User *User `validate:"required"`

	// We support 3 scopes currently
	//  - org
	//  - user
	//  - workflow
	Scope string `validate:"oneof=user org workflow"`

	OrgID      string
	WorkflowID string
}
