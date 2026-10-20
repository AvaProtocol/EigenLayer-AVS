// retired_wallet_funds — list legacy-factory wallets that still hold funds, so
// operators can handle them by hand.
//
// The 20261020-retire-legacy-factory-wallets migration marks every derived
// wallet whose factory is not MA v2 as Retired: it is kept on disk but never
// listed again. This report walks the same records and checks each one's
// native balance and every whitelisted token on its chain. It selects records
// that are already retired AND records the migration would retire, so it works
// on a backup taken before the release (a preview) as well as after it.
//
// READ-ONLY in intent, but BadgerDB takes an exclusive directory lock, so run
// it against a COPY or backup of the gateway DB, never the live directory.
//
// Usage:
//
//	go run ./scripts/retired_wallet_funds \
//	  --gateway-path /path/to/gateway-db-copy \
//	  --rpc 1="$ETH_RPC_URL" --rpc 8453="$BASE_RPC_URL" \
//	  --rpc 11155111="$SEPOLIA_RPC_URL" --rpc 84532="$BASE_SEPOLIA_RPC_URL"
//
// Flags: --owner limits the walk to one owner; --all prints unfunded wallets
// too. Chains without an --rpc are reported as unchecked, never as empty.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"text/tabwriter"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine/tokenwhitelist"
	"github.com/AvaProtocol/EigenLayer-AVS/migrations"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// whitelistFiles mirrors taskengine's catalog filename → chain mapping.
var whitelistFiles = map[uint64]string{
	taskengine.ChainIDEthereum:    "ethereum.json",
	taskengine.ChainIDSepolia:     "sepolia.json",
	taskengine.ChainIDBase:        "base.json",
	taskengine.ChainIDBaseSepolia: "base-sepolia.json",
	taskengine.ChainIDBNBMainnet:  "bnb-mainnet.json",
}

var balanceOfSelector = common.FromHex("0x70a08231")

type token struct {
	Address  common.Address
	Symbol   string
	Decimals int
}

type wallet struct {
	ChainID   uint64
	Owner     string
	Address   common.Address
	Factory   string
	Salt      string
	Workflows int
	State     string // "retired" or "pending" (the migration has not run on this copy)
	Holdings  []string
	Checked   bool
	Err       string
}

type rpcFlags map[uint64]string

func (r rpcFlags) String() string { return fmt.Sprint(map[uint64]string(r)) }

func (r rpcFlags) Set(value string) error {
	chain, url, ok := strings.Cut(value, "=")
	if !ok || url == "" {
		return fmt.Errorf("--rpc wants <chainID>=<url>, got %q", value)
	}
	id, err := strconv.ParseUint(chain, 10, 64)
	if err != nil {
		return fmt.Errorf("--rpc chain id %q: %w", chain, err)
	}
	r[id] = url
	return nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "retired_wallet_funds failed: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	rpcs := rpcFlags{}
	gatewayPath := flag.String("gateway-path", "", "Path to a COPY of the gateway BadgerDB directory (not the live DB)")
	ownerFilter := flag.String("owner", "", "Only this owner EOA (optional)")
	all := flag.Bool("all", false, "Also print wallets that hold nothing")
	concurrency := flag.Int("concurrency", 8, "Parallel wallet balance reads")
	flag.Var(rpcs, "rpc", "<chainID>=<http RPC URL>; repeat per chain")
	flag.Parse()
	if *gatewayPath == "" {
		return fmt.Errorf("--gateway-path is required")
	}

	db, err := storage.NewWithPath(*gatewayPath)
	if err != nil {
		return fmt.Errorf("open BadgerDB at %s: %w", *gatewayPath, err)
	}
	defer db.Close()

	wallets, err := collect(db, strings.ToLower(*ownerFilter))
	if err != nil {
		return err
	}

	clients := map[uint64]*ethclient.Client{}
	for id, url := range rpcs {
		client, dialErr := ethclient.Dial(url)
		if dialErr != nil {
			return fmt.Errorf("dial RPC for chain %d: %w", id, dialErr)
		}
		clients[id] = client
	}
	tokens, err := loadTokens()
	if err != nil {
		return err
	}

	checkAll(wallets, clients, tokens, *concurrency)
	return report(wallets, *all)
}

// collect returns every record the migration retires or would retire.
func collect(db storage.Storage, ownerFilter string) ([]*wallet, error) {
	items, err := db.GetByPrefix([]byte("w:"))
	if err != nil {
		return nil, fmt.Errorf("scan wallets: %w", err)
	}
	var out []*wallet
	for _, item := range items {
		parts := strings.Split(string(item.Key), ":")
		if len(parts) != 4 {
			continue
		}
		chainID, err := strconv.ParseUint(parts[1], 10, 64)
		if err != nil {
			continue
		}
		owner := parts[2]
		if ownerFilter != "" && owner != ownerFilter {
			continue
		}
		record := &model.SmartWallet{}
		if err := record.FromStorageData(item.Value); err != nil {
			fmt.Fprintf(os.Stderr, "skip unreadable record %s: %v\n", item.Key, err)
			continue
		}
		state := ""
		switch {
		case record.Retired:
			state = "retired"
		case migrations.IsLegacyFactoryWallet(record):
			state = "pending"
		default:
			continue
		}
		w := &wallet{ChainID: chainID, Owner: owner, Address: *record.Address, Factory: "none", Salt: "none", State: state}
		if record.Factory != nil {
			w.Factory = strings.ToLower(record.Factory.Hex())
		}
		if record.Salt != nil {
			w.Salt = record.Salt.String()
		}
		prefix := fmt.Sprintf("u:%s:%s:", owner, strings.ToLower(record.Address.Hex()))
		_ = db.IterateKeysOnly([]byte(prefix), func([]byte) error { w.Workflows++; return nil })
		out = append(out, w)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ChainID != out[j].ChainID {
			return out[i].ChainID < out[j].ChainID
		}
		if out[i].Owner != out[j].Owner {
			return out[i].Owner < out[j].Owner
		}
		return strings.ToLower(out[i].Address.Hex()) < strings.ToLower(out[j].Address.Hex())
	})
	return out, nil
}

func loadTokens() (map[uint64][]token, error) {
	out := map[uint64][]token{}
	for chainID, file := range whitelistFiles {
		data, err := tokenwhitelist.FS.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("read token whitelist %s: %w", file, err)
		}
		var entries []struct {
			ID       string `json:"id"`
			Symbol   string `json:"symbol"`
			Decimals int    `json:"decimals"`
		}
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil, fmt.Errorf("parse token whitelist %s: %w", file, err)
		}
		for _, e := range entries {
			if common.IsHexAddress(e.ID) {
				out[chainID] = append(out[chainID], token{Address: common.HexToAddress(e.ID), Symbol: e.Symbol, Decimals: e.Decimals})
			}
		}
	}
	return out, nil
}

func checkAll(wallets []*wallet, clients map[uint64]*ethclient.Client, tokens map[uint64][]token, concurrency int) {
	if concurrency < 1 {
		concurrency = 1
	}
	jobs := make(chan *wallet)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for w := range jobs {
				client, ok := clients[w.ChainID]
				if !ok {
					continue
				}
				check(w, client, tokens[w.ChainID])
			}
		}()
	}
	for _, w := range wallets {
		jobs <- w
	}
	close(jobs)
	wg.Wait()
}

func check(w *wallet, client *ethclient.Client, tokens []token) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	native, err := client.BalanceAt(ctx, w.Address, nil)
	if err != nil {
		w.Err = fmt.Sprintf("native balance: %v", err)
		return
	}
	if native.Sign() > 0 {
		w.Holdings = append(w.Holdings, formatUnits(native, 18)+" native")
	}
	for _, t := range tokens {
		data := append(append([]byte{}, balanceOfSelector...), common.LeftPadBytes(w.Address.Bytes(), 32)...)
		raw, callErr := client.CallContract(ctx, ethereum.CallMsg{To: &t.Address, Data: data}, nil)
		if callErr != nil || len(raw) < 32 {
			continue // a token that does not answer balanceOf is not a holding
		}
		amount := new(big.Int).SetBytes(raw[:32])
		if amount.Sign() > 0 {
			w.Holdings = append(w.Holdings, formatUnits(amount, t.Decimals)+" "+t.Symbol)
		}
	}
	w.Checked = true
}

func report(wallets []*wallet, all bool) error {
	funded, unchecked, failed := 0, 0, 0
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "CHAIN\tOWNER\tWALLET\tFACTORY\tSALT\tWORKFLOWS\tSTATE\tHOLDINGS")
	for _, w := range wallets {
		holdings := strings.Join(w.Holdings, ", ")
		switch {
		case w.Err != "":
			failed++
			holdings = "ERROR: " + w.Err
		case !w.Checked:
			unchecked++
			holdings = "unchecked (no --rpc for this chain)"
		case len(w.Holdings) > 0:
			funded++
		case !all:
			continue
		default:
			holdings = "-"
		}
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			w.ChainID, w.Owner, strings.ToLower(w.Address.Hex()), w.Factory, w.Salt, w.Workflows, w.State, holdings)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "\nlegacy wallets: %d  funded: %d  unchecked: %d  errors: %d\n", len(wallets), funded, unchecked, failed)
	return nil
}

// formatUnits renders amount / 10^decimals without trailing zeros.
func formatUnits(amount *big.Int, decimals int) string {
	if decimals <= 0 {
		return amount.String()
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)
	whole, frac := new(big.Int).QuoRem(amount, scale, new(big.Int))
	if frac.Sign() == 0 {
		return whole.String()
	}
	fracStr := strings.TrimRight(fmt.Sprintf("%0*s", decimals, frac.String()), "0")
	return whole.String() + "." + fracStr
}
