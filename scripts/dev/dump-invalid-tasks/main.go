// dump-invalid-tasks — read-only diagnostic that scans a gateway BadgerDB for
// Enabled workflows failing validation, printing task id / chain / created-at /
// owner / error so operators can decide whether to repair or delete them
// (e.g. the legacy "send eth" node-name and missing-trigger-config cohorts).
//
// READ-ONLY in intent, but BadgerDB takes an exclusive directory lock, so run
// this against a COPY/backup of the gateway DB (e.g. /data/gateway-backup),
// NOT the live directory while the gateway is running.
//
// Usage:
//
//	go run ./scripts/dev/dump-invalid-tasks --gateway-path /path/to/gateway-db-copy
package main

import (
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
	"google.golang.org/protobuf/encoding/protojson"
)

// ulidTime decodes the millisecond timestamp embedded in a ULID's first 10
// Crockford-base32 characters (task IDs are ULIDs).
func ulidTime(id string) string {
	const enc = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"
	u := strings.ToUpper(id)
	if len(u) < 10 {
		return "?"
	}
	var ms int64
	for _, c := range u[:10] {
		i := strings.IndexRune(enc, c)
		if i < 0 {
			return "?"
		}
		ms = ms*32 + int64(i)
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02 15:04")
}

func main() {
	gatewayPath := flag.String("gateway-path", "", "Path to a COPY of the gateway BadgerDB directory (not the live DB)")
	flag.Parse()
	if *gatewayPath == "" {
		log.Fatalf("--gateway-path is required (point it at a backup/copy, not the live DB)")
	}

	db, err := storage.NewWithPath(*gatewayPath)
	if err != nil {
		log.Fatalf("open BadgerDB at %s: %v", *gatewayPath, err)
	}
	defer db.Close()

	items, err := db.GetByPrefix([]byte("t:"))
	if err != nil {
		log.Fatalf("scan workflows: %v", err)
	}

	fmt.Printf("%-28s %-9s %-16s %-44s %s\n", "TaskID", "Chain", "CreatedUTC", "Owner", "Error")
	fmt.Println(strings.Repeat("-", 150))

	scanned, invalid := 0, 0
	for _, it := range items {
		// Only Enabled workflow rows: t:<chain>:a:<id>. ULIDs contain no
		// colons, so a 4-way split is exact.
		parts := strings.SplitN(string(it.Key), ":", 4)
		if len(parts) != 4 || parts[0] != "t" || parts[1] == "" || parts[2] != "a" {
			continue
		}
		scanned++

		task := &model.Workflow{Task: &avsproto.Task{}}
		if derr := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(it.Value, task); derr != nil {
			invalid++
			fmt.Printf("%-28s %-9s %-16s %-44s %s\n", parts[3], parts[1], "?", "?", "DECODE ERROR: "+derr.Error())
			continue
		}

		if verr := task.ValidateWithError(); verr != nil {
			invalid++
			owner := task.GetOwner()
			if owner == "" {
				owner = task.GetSmartWalletAddress()
			}
			fmt.Printf("%-28s %-9s %-16s %-44s %s\n",
				task.GetId(), parts[1], ulidTime(task.GetId()), owner, verr.Error())
		}
	}

	fmt.Println(strings.Repeat("-", 150))
	fmt.Printf("Scanned %d Enabled workflows; %d invalid.\n", scanned, invalid)
}
