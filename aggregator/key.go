package aggregator

import (
	"fmt"
	"strconv"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/core/auth"
	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/ethereum/go-ethereum/common"
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
		return fmt.Errorf("cannot initialize aggregator from config: %w", err)
	}

	if opt.Subject == "" {
		return fmt.Errorf("error: subject cannot be empty")
	}

	if !common.IsHexAddress(opt.Subject) {
		return fmt.Errorf("error: subject must be a valid 0x-prefixed EOA address (got %q). The subject is used as the owner identity bound to this API key.", opt.Subject)
	}

	if len(opt.Roles) < 1 {
		return fmt.Errorf("error: at least one role is required")
	}

	roles := make([]auth.ApiRole, len(opt.Roles))
	for i, v := range opt.Roles {
		roles[i] = auth.ApiRole(v)
	}

	// The verifier (aggregator/auth.go::verifyAuth) requires the JWT to have an
	// `aud` claim containing the smart wallet chain ID. r.chainID in the
	// verifier is sourced from the smart wallet RPC (see rpc_server.go), not
	// the EigenLayer RPC, so we must use SmartWallet.ChainID here too — using
	// the EigenLayer chain ID would silently break cross-chain configs (e.g.
	// EigenLayer on Ethereum + SmartWallet on Base). config.NewConfig already
	// populated SmartWallet.ChainID at startup, so no extra RPC dial is needed.
	if nodeConfig.SmartWallet == nil || nodeConfig.SmartWallet.ChainID == 0 {
		return fmt.Errorf("smart wallet chain ID not populated in config; cannot build audience claim")
	}
	audienceChainID := strconv.FormatInt(nodeConfig.SmartWallet.ChainID, 10)

	claims := &auth.APIClaim{
		RegisteredClaims: &jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour * 24 * 365 * 10)),
			Issuer:    auth.Issuer,
			Subject:   opt.Subject,
			Audience:  jwt.ClaimStrings{audienceChainID},
		},
		Roles: roles,
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	ss, err := token.SignedString(aggregator.config.JwtSecret)

	fmt.Println(ss)

	return err
}
