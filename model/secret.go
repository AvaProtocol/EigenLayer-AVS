package model

type Secret struct {
	Name  string `validate:"required,min=1,max=255"`
	Value string `validate:"required,min=1,max=4096"`

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
