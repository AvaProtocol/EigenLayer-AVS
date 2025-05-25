package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type MigrationChange struct {
	Type        string // "storage_key", "struct_field", "type_change", etc.
	Description string
	Details     string
}

func main() {
	if len(os.Args) != 2 {
		fmt.Println("Usage: go run scripts/migration/create_migration.go <old_branch>")
		fmt.Println("Example: go run scripts/migration/create_migration.go main")
		fmt.Println("\nNote: Run this script after running compare_storage_structure.go")
		os.Exit(1)
	}

	oldBranch := os.Args[1]
	changes := detectChanges(oldBranch)
	if len(changes) == 0 {
		fmt.Println("No changes requiring migration detected.")
		os.Exit(0)
	}

	// Generate migration file
	timestamp := time.Now().Format("20060102-150405")
	description := generateDescription(changes)
	filename := fmt.Sprintf("%s-%s.go", timestamp, sanitizeDescription(description))
	filepath := filepath.Join("migrations", filename)

	content := generateMigrationContent(timestamp, description, changes)
	if err := os.WriteFile(filepath, []byte(content), 0644); err != nil {
		fmt.Printf("Error creating migration file: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Created migration file: %s\n", filepath)
	fmt.Println("\nNext steps:")
	fmt.Println("1. Review the generated migration file")
	fmt.Println("2. Implement the migration logic in the Migrate() function")
	fmt.Println("3. Add the migration to migrations/migrations.go")
	fmt.Println("4. Test the migration thoroughly")
}

func detectChanges(oldBranch string) []MigrationChange {
	// Get the workspace root directory
	workspaceRoot, err := os.Getwd()
	if err != nil {
		fmt.Printf("Error getting current directory: %v\n", err)
		os.Exit(1)
	}

	// Run compare_storage_structure.go and capture both stdout and stderr
	cmd := exec.Command("go", "run", "scripts/compare_storage_structure.go", oldBranch)
	cmd.Dir = workspaceRoot             // Ensure we're running from the workspace root
	output, err := cmd.CombinedOutput() // Use CombinedOutput to capture both stdout and stderr
	if err != nil {
		fmt.Printf("Error running comparison script:\n%s\nError: %v\n", string(output), err)
		fmt.Println("\nPossible causes:")
		fmt.Println("1. The branch '" + oldBranch + "' might not exist")
		fmt.Println("2. You might not be in the correct directory")
		fmt.Println("3. The comparison script might have failed")
		fmt.Println("\nTry running these commands manually:")
		fmt.Println("  git fetch origin")
		fmt.Println("  git branch -a | grep " + oldBranch)
		fmt.Println("  go run scripts/compare_storage_structure.go " + oldBranch)
		os.Exit(1)
	}

	var changes []MigrationChange
	lines := strings.Split(string(output), "\n")

	// Parse the output to detect changes
	for i, line := range lines {
		switch {
		case strings.Contains(line, "Storage key structure changes detected"):
			changes = append(changes, MigrationChange{
				Type:        "storage_key",
				Description: "Storage key structure changes",
				Details:     extractDetails(lines[i:], "Storage key"),
			})
		case strings.Contains(line, "Incompatible data structure changes detected"):
			changes = append(changes, MigrationChange{
				Type:        "data_structure",
				Description: "Data structure changes",
				Details:     extractDetails(lines[i:], "Data structure"),
			})
		case strings.Contains(line, "Required field changes detected"):
			changes = append(changes, MigrationChange{
				Type:        "required_field",
				Description: "Required field changes",
				Details:     extractDetails(lines[i:], "Required field"),
			})
		}
	}

	return changes
}

func extractDetails(lines []string, prefix string) string {
	var details []string
	for _, line := range lines {
		if strings.HasPrefix(line, "- ") {
			details = append(details, strings.TrimPrefix(line, "- "))
		}
		if len(details) > 0 && !strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, " ") {
			break
		}
	}
	return strings.Join(details, "\n")
}

func generateDescription(changes []MigrationChange) string {
	var parts []string
	for _, change := range changes {
		parts = append(parts, change.Type)
	}
	return strings.Join(parts, "-")
}

func sanitizeDescription(desc string) string {
	// Convert to lowercase and replace spaces with hyphens
	desc = strings.ToLower(desc)
	desc = strings.ReplaceAll(desc, " ", "-")
	// Remove any non-alphanumeric characters except hyphens
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			return r
		}
		return -1
	}, desc)
}

func generateMigrationContent(timestamp, description string, changes []MigrationChange) string {
	var migrationCode strings.Builder

	// Write package and imports
	migrationCode.WriteString(`package migrations

import (
	"context"
	"fmt"
	"log"

	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/AvaProtocol/EigenLayer-AVS/storage"
)

// Migration` + timestamp + ` handles ` + description + `
type Migration` + timestamp + ` struct {
	description string
}

func NewMigration` + timestamp + `() *Migration` + timestamp + ` {
	return &Migration` + timestamp + `{
		description: "` + description + `",
	}
}

func (m *Migration` + timestamp + `) Description() string {
	return m.description
}

func (m *Migration` + timestamp + `) Migrate(ctx context.Context, db storage.Storage) error {
	log.Printf("Starting migration: %s\n", m.description)
	
	// TODO: Implement migration logic based on the following changes:
`)

	// Add comments about detected changes
	for _, change := range changes {
		migrationCode.WriteString("\n\t// " + change.Type + " changes:\n")
		for _, detail := range strings.Split(change.Details, "\n") {
			migrationCode.WriteString("\t// - " + detail + "\n")
		}
	}

	// Add example migration code based on change types
	migrationCode.WriteString(`
	// Example migration code:
	// 1. For storage key changes:
	//    - Update key formats
	//    - Migrate data to new keys
	//    - Remove old keys if needed
	//
	// 2. For data structure changes:
	//    - Update struct fields
	//    - Convert data to new format
	//    - Handle required fields
	//
	// 3. For type changes:
	//    - Convert data types
	//    - Update validation logic
	//
	// Example:
	// keys, err := db.ListKeys(ctx, "old-prefix:")
	// if err != nil {
	//     return fmt.Errorf("failed to list keys: %w", err)
	// }
	//
	// for _, key := range keys {
	//     data, err := db.Get(ctx, key)
	//     if err != nil {
	//         return fmt.Errorf("failed to get data for key %s: %w", key, err)
	//     }
	//
	//     // Convert data to new format
	//     newData := convertToNewFormat(data)
	//
	//     // Store with new key format
	//     newKey := convertToNewKey(key)
	//     if err := db.Set(ctx, newKey, newData); err != nil {
	//         return fmt.Errorf("failed to set new key %s: %w", newKey, err)
	//     }
	//
	//     // Optionally remove old key
	//     if err := db.Delete(ctx, key); err != nil {
	//         return fmt.Errorf("failed to delete old key %s: %w", key, err)
	//     }
	// }

	return nil
}
`)

	return migrationCode.String()
}
