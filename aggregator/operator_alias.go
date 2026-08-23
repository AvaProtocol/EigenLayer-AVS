package aggregator

import (
	"context"
	"fmt"
	"sync"
	"time"

	sdklogging "github.com/Layr-Labs/eigensdk-go/logging"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/chainio/apconfig"
)

// Operators are not required to run with the key behind their registered
// EigenLayer address. They may declare an alias — a hot key held by the
// running node — against that address in the APConfig contract, keeping
// the registered key cold. Two of the three operators approved on
// mainnet do exactly this, so the aggregator cannot authenticate the
// operator plane by comparing a recovered signature to the claimed
// address alone; it has to resolve the same mapping the operator
// declared.
//
// A multi-chain gateway talks to operators registered on more than one
// AVS chain. Aliases live on that chain's APConfig, not on a single
// "the" APConfig: the Sepolia operator's mapping is on the testnet
// deployment, the Ethereum operators' on mainnet. Binding only the
// chain behind eth_rpc_url (Sepolia, in production) would refuse the
// two alias-key mainnet operators at their first heartbeat. Every
// configured AVS-chain RPC is therefore a source.

const (
	// aliasCacheTTL bounds how stale a mapping may get. Declaring an
	// alias is an on-chain transaction, so it changes rarely, and this
	// keeps a per-RPC chain call off the auth path.
	aliasCacheTTL = 5 * time.Minute

	// aliasLookupTimeout caps a single APConfig call so a slow RPC
	// endpoint cannot stall operator authentication.
	aliasLookupTimeout = 10 * time.Second
)

type aliasEntry struct {
	alias     common.Address
	fetchedAt time.Time
}

type aliasSource struct {
	chainID  int64
	name     string
	contract *apconfig.APConfig
	client   *ethclient.Client
	address  common.Address
}

type operatorAliasResolver struct {
	sources []aliasSource
	// skipped is every bind that failed at startup. Those chains are
	// not consulted for aliases, so ping reports them as down rather
	// than omitting them (which would look healthy).
	skipped []apconfigHealth
	logger  sdklogging.Logger

	mu    sync.Mutex
	cache map[common.Address]aliasEntry
}

func newOperatorAliasResolver(sources []aliasSource, logger sdklogging.Logger) *operatorAliasResolver {
	return &operatorAliasResolver{
		sources: sources,
		logger:  logger,
		cache:   make(map[common.Address]aliasEntry),
	}
}

// aliasBindAttempt is one APConfig bind, success or not. mergeAliasSources
// skips failures so a missing testnet deployment cannot take the gateway
// down when mainnet APConfig (where the alias-key operators actually
// declared) is reachable.
type aliasBindAttempt struct {
	name string
	src  aliasSource
	err  error
}

// mergeAliasSources keeps every successful bind and logs the rest.
// Startup fails only when nothing bound — that is the case that would
// refuse the whole operator fleet.
func mergeAliasSources(logger sdklogging.Logger, attempts ...aliasBindAttempt) ([]aliasSource, []apconfigHealth, error) {
	sources := make([]aliasSource, 0, len(attempts))
	skipped := make([]apconfigHealth, 0)
	var lastErr error
	for _, a := range attempts {
		if a.err != nil {
			lastErr = a.err
			if logger != nil {
				logger.Warn("skipping APConfig source for operator alias resolution",
					"source", a.name, "error", a.err)
			}
			addr := ""
			if a.src.address != (common.Address{}) {
				addr = a.src.address.Hex()
			}
			skipped = append(skipped, apconfigHealth{
				ChainID: a.src.chainID,
				Name:    a.name,
				Address: addr,
				Status:  deepHealthDown,
				Error:   a.err.Error(),
			})
			continue
		}
		sources = append(sources, a.src)
	}
	if len(sources) == 0 {
		if lastErr != nil {
			return nil, skipped, fmt.Errorf("operator alias resolution cannot start: no APConfig source bound: %w", lastErr)
		}
		return nil, skipped, fmt.Errorf("operator alias resolution cannot start: no APConfig source bound")
	}
	return sources, skipped, nil
}

// bindAliasSources dials every APConfig this aggregator must consult.
//
// avsRPC is the top-level EigenLayer registration RPC (eth_rpc_url).
// When a chain registry lists Ethereum mainnet, that chain's RPC is
// bound too — production's two alias-key operators declared on mainnet
// APConfig, not on Sepolia.
//
// An individual source with no contract code is skipped. v4.17.3
// pointed at a stale Sepolia proxy, fail-closed, and took the gateway
// down with it. The live Sepolia address is compiled in; this skip is
// the belt so a future empty bind cannot do that again. Failure is
// reserved for "no source bound at all".
func bindAliasSources(
	ctx context.Context,
	avsRPC *ethclient.Client,
	registry *ChainRegistry,
	logger sdklogging.Logger,
) ([]aliasSource, []apconfigHealth, error) {
	attempts := make([]aliasBindAttempt, 0, 2)
	var avsChainID int64

	if avsRPC == nil {
		attempts = append(attempts, aliasBindAttempt{
			name: "avs",
			err:  fmt.Errorf("AVS-chain RPC is required to resolve operator aliases"),
		})
	} else {
		id, err := avsRPC.ChainID(ctx)
		if err != nil {
			attempts = append(attempts, aliasBindAttempt{
				name: "avs",
				err:  fmt.Errorf("reading AVS chain id for operator alias resolution: %w", err),
			})
		} else {
			avsChainID = id.Int64()
			addr := apconfig.AddressForChain(id)
			src, bindErr := bindOneAliasSource(ctx, avsRPC, avsChainID, "avs", addr)
			if bindErr != nil {
				src = aliasSource{chainID: avsChainID, name: "avs", address: addr}
			}
			attempts = append(attempts, aliasBindAttempt{name: "avs", src: src, err: bindErr})
		}
	}

	if registry != nil && avsChainID != 1 {
		if chainCfg, cfgErr := registry.GetChainConfig(1); cfgErr == nil && chainCfg != nil &&
			chainCfg.SmartWallet != nil && chainCfg.SmartWallet.EthRpcUrl != "" {
			ethRPC, dialErr := ethclient.DialContext(ctx, chainCfg.SmartWallet.EthRpcUrl)
			if dialErr != nil {
				attempts = append(attempts, aliasBindAttempt{
					name: "ethereum",
					err:  fmt.Errorf("dialing Ethereum RPC for mainnet APConfig: %w", dialErr),
				})
			} else {
				mainnet, bindErr := bindOneAliasSource(ctx, ethRPC, 1, "ethereum", apconfig.MainnetAddress)
				if bindErr != nil {
					ethRPC.Close()
					attempts = append(attempts, aliasBindAttempt{
						name: "ethereum",
						src:  aliasSource{chainID: 1, name: "ethereum", address: apconfig.MainnetAddress},
						err:  bindErr,
					})
				} else {
					attempts = append(attempts, aliasBindAttempt{name: "ethereum", src: mainnet})
					if logger != nil {
						logger.Info("operator alias resolution will consult mainnet APConfig",
							"apconfig_address", apconfig.MainnetAddress.Hex())
					}
				}
			}
		}
	}

	return mergeAliasSources(logger, attempts...)
}

func bindOneAliasSource(ctx context.Context, rpc *ethclient.Client, chainID int64, name string, address common.Address) (aliasSource, error) {
	code, err := rpc.CodeAt(ctx, address, nil)
	if err != nil {
		return aliasSource{}, fmt.Errorf("reading APConfig code at %s on %s (chain %d): %w", address.Hex(), name, chainID, err)
	}
	if len(code) == 0 {
		return aliasSource{}, fmt.Errorf("no contract code at APConfig %s on %s (chain %d)", address.Hex(), name, chainID)
	}
	contract, err := apconfig.NewAPConfig(address, rpc)
	if err != nil {
		return aliasSource{}, fmt.Errorf("binding APConfig at %s on %s: %w", address.Hex(), name, err)
	}
	return aliasSource{
		chainID:  chainID,
		name:     name,
		contract: contract,
		client:   rpc,
		address:  address,
	}, nil
}

// aliasFor returns the alias key an operator declared, or the zero
// address when it declared none and is expected to sign with its own key.
//
// Every configured APConfig is consulted. A non-zero mapping on any of
// them wins — operators register on one AVS chain, not both.
func (r *operatorAliasResolver) aliasFor(ctx context.Context, operator common.Address) (common.Address, error) {
	r.mu.Lock()
	entry, cached := r.cache[operator]
	r.mu.Unlock()

	if cached && time.Since(entry.fetchedAt) < aliasCacheTTL {
		return entry.alias, nil
	}

	if len(r.sources) == 0 {
		if cached {
			return entry.alias, nil
		}
		return common.Address{}, fmt.Errorf("APConfig contract is not configured")
	}

	lookupCtx, cancel := context.WithTimeout(ctx, aliasLookupTimeout)
	defer cancel()

	var lastErr error
	found := common.Address{}
	anyOK := false
	anyErr := false
	for _, src := range r.sources {
		if src.contract == nil {
			continue
		}
		alias, err := src.contract.GetAlias(&bind.CallOpts{Context: lookupCtx}, operator)
		if err != nil {
			anyErr = true
			lastErr = fmt.Errorf("%s: %w", src.name, err)
			continue
		}
		anyOK = true
		if alias != (common.Address{}) {
			found = alias
			break
		}
	}

	if found == (common.Address{}) && anyErr {
		// Incomplete view: at least one source failed, and none of the
		// successful ones had a mapping. Do not cache zero — that would
		// treat a mainnet alias-key operator as "no alias" because
		// Sepolia answered first with empty. Serve a stale mapping if
		// we have one; otherwise fail the RPC.
		if cached {
			r.logger.Warn("APConfig alias lookup incomplete, falling back to cached mapping",
				"operator", operator.Hex(), "error", lastErr)
			return entry.alias, nil
		}
		return common.Address{}, fmt.Errorf("looking up alias for %s: %w", operator.Hex(), lastErr)
	}
	if !anyOK {
		if cached {
			r.logger.Warn("APConfig alias lookup failed, falling back to cached mapping",
				"operator", operator.Hex(), "error", lastErr)
			return entry.alias, nil
		}
		return common.Address{}, fmt.Errorf("looking up alias for %s: %w", operator.Hex(), lastErr)
	}

	r.mu.Lock()
	r.cache[operator] = aliasEntry{alias: found, fetchedAt: time.Now()}
	r.mu.Unlock()

	return found, nil
}

// ping reports whether each APConfig source still answers. Used by
// /health/deep so a dead AVS-chain RPC is visible before operators
// start failing auth.
func (r *operatorAliasResolver) ping(ctx context.Context) []apconfigHealth {
	if r == nil {
		return nil
	}
	out := make([]apconfigHealth, 0, len(r.sources)+len(r.skipped))
	out = append(out, r.skipped...)
	for _, src := range r.sources {
		h := apconfigHealth{
			ChainID: src.chainID,
			Name:    src.name,
			Address: src.address.Hex(),
			Status:  deepHealthOK,
		}
		if src.client == nil {
			h.Status = deepHealthDown
			h.Error = "no rpc client"
			out = append(out, h)
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, aliasLookupTimeout)
		_, err := src.client.CodeAt(callCtx, src.address, nil)
		cancel()
		if err != nil {
			h.Status = deepHealthDown
			h.Error = err.Error()
		}
		out = append(out, h)
	}
	return out
}
