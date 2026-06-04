// count-gateway-keys — print per-prefix key counts for a BadgerDB
// directory. Used by run-merge-rehearsal.sh to verify what the merge
// tool wrote and let the operator compare counts against the live SDK
// queries (yarn start listWorkflows / getExecution).
//
// Usage:
//
//	go run scripts/dev/count-gateway-keys.go --gateway-path /tmp/...
package main

import (
	"flag"
	"fmt"
	"log"
	"sort"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

func main() {
	gatewayPath := flag.String("gateway-path", "", "Path to the gateway BadgerDB directory")
	flag.Parse()
	if *gatewayPath == "" {
		log.Fatalf("--gateway-path is required")
	}

	db, err := storage.NewWithPath(*gatewayPath)
	if err != nil {
		log.Fatalf("open BadgerDB at %s: %v", *gatewayPath, err)
	}
	defer db.Close()

	// Bucket every key by its prefix (everything up to first ':') and
	// then by chain (next segment if numeric) so the post-merge
	// distribution is visible at a glance.
	type bucket struct {
		prefix  string
		chainID string // "_" when not present or non-numeric
		count   int64
	}
	counts := map[string]*bucket{}

	if err := db.IterateKeysOnly([]byte(""), func(key []byte) error {
		s := string(key)
		prefix := s
		rest := ""
		for i, c := range s {
			if c == ':' {
				prefix = s[:i+1]
				rest = s[i+1:]
				break
			}
		}
		chainID := "_"
		// Chain segment is everything up to the next ':' if it parses
		// as an integer.
		for i, c := range rest {
			if c == ':' {
				cand := rest[:i]
				ok := len(cand) > 0
				for _, ch := range cand {
					if ch < '0' || ch > '9' {
						ok = false
						break
					}
				}
				if ok {
					chainID = cand
				}
				break
			}
		}
		bucketKey := prefix + "|" + chainID
		b, found := counts[bucketKey]
		if !found {
			b = &bucket{prefix: prefix, chainID: chainID}
			counts[bucketKey] = b
		}
		b.count++
		return nil
	}); err != nil {
		log.Fatalf("iterate keys: %v", err)
	}

	// Sort by prefix, then chain
	type row struct{ b *bucket }
	rows := make([]row, 0, len(counts))
	for _, b := range counts {
		rows = append(rows, row{b})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].b.prefix != rows[j].b.prefix {
			return rows[i].b.prefix < rows[j].b.prefix
		}
		return rows[i].b.chainID < rows[j].b.chainID
	})

	fmt.Printf("%-30s %-12s %12s\n", "Prefix", "ChainID", "Count")
	fmt.Println("----------------------------- ------------ ------------")
	var total int64
	for _, r := range rows {
		fmt.Printf("%-30s %-12s %12d\n", r.b.prefix, r.b.chainID, r.b.count)
		total += r.b.count
	}
	fmt.Println("----------------------------- ------------ ------------")
	fmt.Printf("%-30s %-12s %12d\n", "TOTAL", "", total)
}
