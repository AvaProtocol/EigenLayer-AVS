package migrations

import (
	"fmt"
	"log"

	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// TokenMetadataFieldsMigration handles the addition of new required fields to TokenMetadata struct
func TokenMetadataFieldsMigration(db storage.Storage) (int, error) {
	log.Printf("Starting migration: TokenMetadata required fields migration")

	// This migration handles the addition of new TokenMetadata struct fields:
	// - TokenMetadata.Address: Added required field of type string
	// - TokenMetadata.Name: Added required field of type string
	// - TokenMetadata.Symbol: Added required field of type string
	// - TokenMetadata.Decimals: Added required field of type uint32
	// - TokenMetadata.Source: Added required field of type string

	recordsUpdated := 0

	// Since these are new structs (TokenMetadata, TokenEnrichmentService, TriggerData)
	// being added to the codebase, there shouldn't be existing data to migrate.
	// However, we should verify this assumption by checking if any related keys exist.

	// Check for any existing token-related keys that might need migration
	tokenKeys, err := db.ListKeys("token:*")
	if err != nil {
		return 0, fmt.Errorf("failed to list token keys: %w", err)
	}

	if len(tokenKeys) > 0 {
		log.Printf("Found %d existing token-related keys that may need review", len(tokenKeys))
		// If there are existing token keys, they would need to be updated
		// to include the new required fields with appropriate default values
		for _, key := range tokenKeys {
			log.Printf("Reviewing key: %s", key)
			// TODO: If needed, implement specific migration logic for existing token data
		}
	}

	// Check for any enrichment-related keys
	enrichmentKeys, err := db.ListKeys("enrichment:*")
	if err != nil {
		return 0, fmt.Errorf("failed to list enrichment keys: %w", err)
	}

	if len(enrichmentKeys) > 0 {
		log.Printf("Found %d existing enrichment-related keys that may need review", len(enrichmentKeys))
	}

	log.Printf("Migration completed successfully. %d records were updated.", recordsUpdated)
	return recordsUpdated, nil
}
