package aggregator

import (
	"fmt"
	"time"

	"github.com/AvaProtocol/ap-avs/core/auth"
	"github.com/AvaProtocol/ap-avs/core/config"
	"github.com/golang-jwt/jwt/v5"
)

type CreateApiKeyOption struct {
	Roles   []string
	Subject string
}

// Create an JWT admin key to manage user task
func CreateAdminKey(configPath string, opt CreateApiKeyOption) error {
	nodeConfig, err := config.NewConfig(configPath)
	if err != nil {
		return fmt.Errorf("failed to parse config file: %s\nMake sure it is exist and a valid yaml file %w.", configPath, err)
	}

	aggregator, err := NewAggregator(nodeConfig)
	if err != nil {
		return fmt.Errorf("cannot initialize aggregrator from config: %w", err)
	}

	if opt.Subject == "" {
		return fmt.Errorf("error: subject cannot be empty")
	}

	if len(opt.Roles) < 1 {
		return fmt.Errorf("error: at least one role is required")
	}

	roles := make([]auth.ApiRole, len(opt.Roles))
	for i, v := range opt.Roles {
		roles[i] = auth.ApiRole(v)
	}

	claims := &auth.APIClaim{
		&jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour * 24 * 365 * 10)),
			Issuer:    "AvaProtocol",
			Subject:   opt.Subject,
		},
		roles,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	ss, err := token.SignedString(aggregator.config.JwtSecret)

	fmt.Println(ss)

	return err
}
