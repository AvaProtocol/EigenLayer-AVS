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
// There is deliberately no MA v2-specific creation function. There was one,
// and the v0.7 cutover made it redundant: Engine.GetWallet now derives through
// aa.EffectiveFactory and stores whatever factory that returns, so on an MA v2
// chain the ordinary path already registers MA v2 wallets under the MA v2
// factory. A parallel creation path would have been a second way to do the
// same thing, differing only in forcing MA v2 on chains pinned to
// simple_account — where forcing it is wrong. Use aa.ProviderForFactory to ask
// what a stored record is.

// MAv2WalletFactory returns the factory address MA v2 wallets are recorded
// under. Distinct from config.DefaultFactoryProxyAddressHex, which is the
// v0.6 SimpleAccountFactory.
func MAv2WalletFactory() common.Address { return aa.MAv2FactoryAddress() }
