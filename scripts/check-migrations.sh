#!/bin/bash

# Script to check migration status in the database
# Usage: ./scripts/check-migrations.sh

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${BLUE}🔍 Checking migration status in database...${NC}"
echo

# Check if we can access the database
if [ ! -f "aggregator.db" ]; then
    echo -e "${RED}❌ Database file 'aggregator.db' not found${NC}"
    echo -e "${YELLOW}   Make sure you're running this from the directory with the database${NC}"
    exit 1
fi

# Use a simple Go script to check migration keys
cat > /tmp/check_migrations.go << 'EOF'
package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/dgraph-io/badger/v4"
)

func main() {
	// Open the database
	opts := badger.DefaultOptions("aggregator.db")
	opts.Logger = nil // Disable logging
	db, err := badger.Open(opts)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	var migrations []string
	
	// Scan for migration keys
	err = db.View(func(txn *badger.Txn) error {
		opts := badger.DefaultIteratorOptions
		opts.PrefetchValues = true
		it := txn.NewIterator(opts)
		defer it.Close()

		prefix := []byte("migration:")
		for it.Seek(prefix); it.ValidForPrefix(prefix); it.Next() {
			item := it.Item()
			key := string(item.Key())
			
			// Get the value to extract timestamp and record count
			var value []byte
			err := item.Value(func(val []byte) error {
				value = make([]byte, len(val))
				copy(value, val)
				return nil
			})
			if err != nil {
				return err
			}
			
			migrations = append(migrations, fmt.Sprintf("%s: %s", key, string(value)))
		}
		return nil
	})
	
	if err != nil {
		log.Fatal(err)
	}

	// Sort migrations by name
	sort.Strings(migrations)

	if len(migrations) == 0 {
		fmt.Println("No migrations found in database")
		return
	}

	fmt.Printf("Found %d applied migrations:\n\n", len(migrations))
	for _, migration := range migrations {
		parts := strings.Split(migration, ": ")
		if len(parts) == 2 {
			migrationName := strings.TrimPrefix(parts[0], "migration:")
			migrationData := parts[1]
			
			// Parse the migration data
			var records, timestamp string
			dataParts := strings.Split(migrationData, ",")
			for _, part := range dataParts {
				if strings.HasPrefix(part, "records=") {
					records = strings.TrimPrefix(part, "records=")
				} else if strings.HasPrefix(part, "ts=") {
					tsStr := strings.TrimPrefix(part, "ts=")
					if ts, err := time.Parse("1", tsStr); err == nil {
						timestamp = ts.Format("2006-01-02 15:04:05")
					} else {
						timestamp = tsStr
					}
				}
			}
			
			fmt.Printf("✅ %s\n", migrationName)
			fmt.Printf("   Records updated: %s\n", records)
			fmt.Printf("   Applied at: %s\n", timestamp)
			fmt.Println()
		}
	}
}
EOF

# Run the Go script
echo -e "${BLUE}📊 Migration Status Report${NC}"
echo "=========================================="

if command -v go >/dev/null 2>&1; then
    cd "$(dirname "$0")/.." # Go to project root
    go run /tmp/check_migrations.go 2>/dev/null || {
        echo -e "${RED}❌ Failed to read database${NC}"
        echo -e "${YELLOW}   Make sure the database is not being used by another process${NC}"
    }
else
    echo -e "${RED}❌ Go not found - cannot check migration status${NC}"
fi

# Clean up
rm -f /tmp/check_migrations.go

echo "=========================================="
echo -e "${BLUE}💡 Note: Completed migrations have been moved to docs/historical-migrations/2025-completed/${NC}"
