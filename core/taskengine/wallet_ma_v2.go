package taskengine

import (
	"github.com/ethereum/go-ethereum/common"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/aa"
)

// Modular Account v2 wallet registration.
//
// MA v2 wallets reuse the existing record and index schema unchanged, because
// `Factory` is already part of both the stored record and the `wsalt:` index
// key. A v0.6 SimpleAccount at salt 0 and an MA v2 account at salt 0 for the
// same owner therefore occupy different index slots and coexist without
// collision — no migration, no separate keyspace, and ListWallets keeps
// working on both.
//
// Registration is not bookkeeping. The Gas Manager sponsorship webhook
// (aggregator/gas_manager_webhook.go) resolves a UserOperation's sender back
// to an owner by scanning `w:<chain>:` and REFUSES senders it cannot find —
// that refusal is the only thing stopping the policy from funding wallets
// created outside this gateway, since policy access control is set to None.
// An MA v2 wallet that is derived but never stored is therefore invisible to
// sponsorship: every operation from it is denied, with a message about an
// unknown sender rather than anything pointing at the missing record.
//
// There is deliberately no MA v2-specific creation function: Engine.GetWallet
// derives under the MA v2 factory and stores it on the record, so the ordinary
// path already registers MA v2 wallets. Use aa.RequireMAv2Factory to ask what a
// stored record is.

// MAv2WalletFactory returns the factory address MA v2 wallets are recorded
// under.
func MAv2WalletFactory() common.Address { return aa.MAv2FactoryAddress() }
