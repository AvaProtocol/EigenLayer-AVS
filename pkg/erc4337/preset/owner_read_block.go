package preset

import (
	"context"
	"math/big"

	"github.com/AvaProtocol/EigenLayer-AVS/core/config"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

type ownerReadBlockKey struct{}

func ownerReadBlock(ctx context.Context) *big.Int {
	v, _ := ctx.Value(ownerReadBlockKey{}).(*big.Int)
	return v
}
func ownerReadBlockTag(ctx context.Context) string {
	if v := ownerReadBlock(ctx); v != nil {
		return hexutil.EncodeBig(v)
	}
	return "latest"
}

// ValidateOwnerUserOpAt pins account identity, owner nonce and funding to the
// same numbered canonical snapshot. Callers verify its hash again before send.
func ValidateOwnerUserOpAt(ctx context.Context, sw *config.SmartWalletConfig, owner common.Address, salt *big.Int, p *PreparedOwnerUserOp, block *big.Int) error {
	return ValidateOwnerUserOp(context.WithValue(ctx, ownerReadBlockKey{}, new(big.Int).Set(block)), sw, owner, salt, p)
}
