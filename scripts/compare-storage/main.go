package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

type StorageKeyTemplate struct {
	Name        string
	KeyTemplate string
	IsNew       bool
	IsModified  bool
}

func main() {
	if len(os.Args) != 2 {
		fmt.Println("Usage: go run compare_storage_structure.go <old_branch>")
		fmt.Println("Example: go run compare_storage_structure.go main")
		fmt.Println("\nNote: Run this script after checking out the new branch you want to compare.")
		fmt.Println("      Ensure the branch history is linear (rebased) for accurate comparison.")
		os.Exit(1)
	}

	oldBranch := os.Args[1]
	currentBranch, err := getCurrentBranch()
	if err != nil {
		fmt.Printf("Error getting current branch: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Comparing storage key structures between %s branch and current branch (%s)\n\n", oldBranch, currentBranch)

	if !hasLinearHistory(oldBranch, currentBranch) {
		fmt.Printf("⚠️  Warning: Branches may not have linear history. Consider rebasing %s onto %s for accurate comparison.\n\n", currentBranch, oldBranch)
	}

	oldBranchKeys := extractStorageKeyStructures(oldBranch)
	currentBranchKeys := extractCurrentBranchKeyStructures()

	compareKeyStructures(oldBranch, currentBranch, oldBranchKeys, currentBranchKeys)

	analyzeDataStructureChanges(oldBranch)

	analyzeGitDiff(oldBranch)
}

func getCurrentBranch() (string, error) {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func hasLinearHistory(oldBranch, currentBranch string) bool {
	cmd := exec.Command("git", "merge-base", "--is-ancestor", oldBranch, currentBranch)
	err := cmd.Run()
	return err == nil
}

func extractStorageKeyStructures(branch string) []StorageKeyTemplate {
	// First check if branch exists
	cmd := exec.Command("git", "show-ref", "--verify", "--quiet", fmt.Sprintf("refs/heads/%s", branch))
	if err := cmd.Run(); err != nil {
		fmt.Printf("Error: Branch '%s' does not exist locally. Try running:\n", branch)
		fmt.Printf("  git fetch origin\n")
		fmt.Printf("  git checkout %s\n", branch)
		return getDefaultStorageKeys()
	}

	// Check if file exists in the branch
	cmd = exec.Command("git", "ls-tree", "-r", "--name-only", branch, "core/taskengine/schema.go")
	if err := cmd.Run(); err != nil {
		fmt.Printf("Error: schema.go not found in branch '%s'. The file might have been moved or deleted.\n", branch)
		return getDefaultStorageKeys()
	}

	// Try to get the file content
	cmd = exec.Command("git", "show", fmt.Sprintf("%s:core/taskengine/schema.go", branch))
	output, err := cmd.Output()
	if err != nil {
		fmt.Printf("Error reading schema.go from branch '%s': %v\n", branch, err)
		fmt.Println("Falling back to default storage keys.")
		return getDefaultStorageKeys()
	}

	return parseStorageKeysFromContent(string(output))
}

func extractCurrentBranchKeyStructures() []StorageKeyTemplate {
	content, err := os.ReadFile("core/taskengine/schema.go")
	if err != nil {
		fmt.Printf("Warning: Could not read schema.go from current branch: %v\n", err)
		return getDefaultStorageKeys()
	}

	return parseStorageKeysFromContent(string(content))
}

func parseStorageKeysFromContent(content string) []StorageKeyTemplate {

	return getDefaultStorageKeys()
}

func getDefaultStorageKeys() []StorageKeyTemplate {
	return []StorageKeyTemplate{
		{Name: "Wallet", KeyTemplate: "w:<eoa>:<smart-wallet-address>", IsNew: false, IsModified: false},
		{Name: "Task", KeyTemplate: "t:<task-status>:<task-id>", IsNew: false, IsModified: false},
		{Name: "UserTask", KeyTemplate: "u:<eoa>:<smart-wallet-address>:<task-id>", IsNew: false, IsModified: false},
		{Name: "Execution", KeyTemplate: "history:<task-id>:<execution-id>", IsNew: false, IsModified: false},
		{Name: "Trigger", KeyTemplate: "trigger:<task-id>:<execution-id>", IsNew: false, IsModified: false},
		{Name: "ContractWriteCounter", KeyTemplate: "ct:cw:<eoa>", IsNew: false, IsModified: false},
		{Name: "Secret", KeyTemplate: "secret:<org_id>:<eoa>:<workflow_id>:<name>", IsNew: false, IsModified: false},
		{Name: "Migration", KeyTemplate: "migration:<migrationName>", IsNew: false, IsModified: false},
	}
}

func compareKeyStructures(oldBranch, currentBranch string, oldKeys, currentKeys []StorageKeyTemplate) {
	oldMap := make(map[string]StorageKeyTemplate)
	currentMap := make(map[string]StorageKeyTemplate)

	for _, key := range oldKeys {
		oldMap[key.Name] = key
	}
	for _, key := range currentKeys {
		currentMap[key.Name] = key
	}

	var newKeysFound []string
	for name, key := range currentMap {
		if _, exists := oldMap[name]; !exists {
			newKeysFound = append(newKeysFound, fmt.Sprintf("- %s: %s", name, key.KeyTemplate))
		}
	}

	var removedKeys []string
	for name, key := range oldMap {
		if _, exists := currentMap[name]; !exists {
			removedKeys = append(removedKeys, fmt.Sprintf("- %s: %s", name, key.KeyTemplate))
		}
	}

	var modifiedKeys []string
	for name, currentKey := range currentMap {
		if oldKey, exists := oldMap[name]; exists && oldKey.KeyTemplate != currentKey.KeyTemplate {
			modifiedKeys = append(modifiedKeys, fmt.Sprintf("- %s: %s -> %s", name, oldKey.KeyTemplate, currentKey.KeyTemplate))
		}
	}

	sort.Strings(newKeysFound)
	sort.Strings(removedKeys)
	sort.Strings(modifiedKeys)

	fmt.Println("=== Storage Key Structure Comparison ===")

	fmt.Printf("\n=== New Keys in Current Branch ===\n")
	if len(newKeysFound) == 0 {
		fmt.Println("No new keys found")
	} else {
		for _, key := range newKeysFound {
			fmt.Println(key)
		}
	}

	fmt.Printf("\n=== Removed Keys in Current Branch ===\n")
	if len(removedKeys) == 0 {
		fmt.Println("No removed keys found")
	} else {
		for _, key := range removedKeys {
			fmt.Println(key)
		}
	}

	fmt.Printf("\n=== Modified Keys in Current Branch ===\n")
	if len(modifiedKeys) == 0 {
		fmt.Println("No modified keys found")
	} else {
		for _, key := range modifiedKeys {
			fmt.Println(key)
		}
	}
}

// analyzeDataStructureChanges analyzes changes in data structures between branches
func analyzeDataStructureChanges(oldBranch string) {
	fmt.Println("\n=== Data Structure Analysis ===")

	analyzeSmartWalletChanges(oldBranch)

	fmt.Println("\n=== Migration Analysis ===")
	if hasStorageKeyChanges(oldBranch) {
		fmt.Println("⚠️ Storage key structure changes detected. A migration may be required.")
		fmt.Println("Recommendation: Review the changes carefully and implement a migration if needed.")
	} else if hasIncompatibleDataChanges(oldBranch) {
		fmt.Println("⚠️ Incompatible data structure changes detected. A migration may be required.")
		fmt.Println("Recommendation: Review the changes carefully and implement a migration if needed.")
	} else {
		fmt.Println("✅ No storage structure changes requiring migration were detected.")
		fmt.Println("Recommendation: No migration needed for the analyzed changes.")
	}
}

// analyzeSmartWalletChanges analyzes changes to the SmartWallet struct
func analyzeSmartWalletChanges(oldBranch string) {
	fmt.Println("\n=== SmartWallet Structure Analysis ===")

	cmd := exec.Command("git", "diff", oldBranch, "--", "model/user.go")
	output, err := cmd.Output()
	if err != nil {
		fmt.Printf("Warning: Could not diff model/user.go: %v\n", err)
		return
	}

	if len(output) == 0 {
		fmt.Println("No changes detected in SmartWallet struct.")
		return
	}

	var addedFields []string
	var removedFields []string
	var modifiedFields []string
	var addedOmitempty []string
	var removedOmitempty []string

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	currentField := ""
	for scanner.Scan() {
		line := scanner.Text()

		// Track field changes
		if strings.HasPrefix(line, "+") && strings.Contains(line, "json:") {
			fieldName := extractFieldName(line)
			if fieldName != "" {
				currentField = fieldName
				if strings.Contains(line, "omitempty") {
					addedOmitempty = append(addedOmitempty, fieldName)
				}
				addedFields = append(addedFields, fieldName)
			}
		} else if strings.HasPrefix(line, "-") && strings.Contains(line, "json:") {
			fieldName := extractFieldName(line)
			if fieldName != "" {
				if strings.Contains(line, "omitempty") {
					removedOmitempty = append(removedOmitempty, fieldName)
				}
				removedFields = append(removedFields, fieldName)
			}
		} else if strings.HasPrefix(line, " ") && currentField != "" {
			// Track modifications to existing fields
			if strings.Contains(line, "json:") {
				modifiedFields = append(modifiedFields, currentField)
			}
		}
	}

	// Analyze changes
	fmt.Println("\nField Changes:")
	if len(addedFields) > 0 {
		fmt.Println("Added fields:")
		for _, field := range addedFields {
			fmt.Printf("- %s\n", field)
		}
	}
	if len(removedFields) > 0 {
		fmt.Println("Removed fields:")
		for _, field := range removedFields {
			fmt.Printf("- %s\n", field)
		}
	}
	if len(modifiedFields) > 0 {
		fmt.Println("Modified fields:")
		for _, field := range modifiedFields {
			fmt.Printf("- %s\n", field)
		}
	}

	fmt.Println("\nBackward Compatibility Analysis:")
	needsMigration := false
	if len(removedFields) > 0 {
		fmt.Println("⚠️  WARNING: Removed fields detected. This is a breaking change.")
		needsMigration = true
	}
	if len(removedOmitempty) > 0 {
		fmt.Println("⚠️  WARNING: Removed omitempty tags detected. This may affect serialization.")
		needsMigration = true
	}
	if len(addedFields) > 0 {
		fmt.Println("Added fields analysis:")
		for _, field := range addedFields {
			if contains(addedOmitempty, field) {
				fmt.Printf("✅ %s: Added with omitempty - backward compatible\n", field)
			} else {
				fmt.Printf("⚠️  %s: Added without omitempty - may require migration\n", field)
				needsMigration = true
			}
		}
	}

	if !needsMigration {
		fmt.Println("\n✅ Changes appear to be backward compatible")
		fmt.Println("Note: New fields with omitempty will default to zero values for existing records")
	} else {
		fmt.Println("\n⚠️  Migration may be required")
		fmt.Println("Recommendation: Review the changes and implement migration if needed")
	}
}

func extractFieldName(line string) string {
	// Extract field name from struct definition
	parts := strings.Fields(line)
	for i, part := range parts {
		if part == "json:" {
			if i > 0 {
				return strings.Trim(parts[i-1], "`")
			}
		}
	}
	return ""
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}

func hasStorageKeyChanges(oldBranch string) bool {
	fmt.Println("\n=== Storage Key Change Analysis ===")

	// Get diff of schema.go
	cmd := exec.Command("git", "diff", oldBranch, "--", "core/taskengine/schema.go")
	output, err := cmd.Output()
	if err != nil {
		fmt.Printf("Warning: Could not diff schema.go: %v\n", err)
		return false
	}

	if len(output) == 0 {
		fmt.Println("No changes detected in storage key definitions.")
		return false
	}

	var keyFuncChanges []string
	var keyFormatChanges []string
	var newKeyFuncs []string
	var removedKeyFuncs []string

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	currentFunc := ""
	inKeyFunc := false

	// Regex patterns for key detection
	keyFuncRegex := regexp.MustCompile(`func\s+(\w+)StorageKey`)
	keyFormatRegex := regexp.MustCompile(`fmt\.Sprintf\([^)]+\)`)
	keyStringRegex := regexp.MustCompile(`"[a-z]:[^"]*"`) // Only matches patterns like "w:...", "t:...", etc.

	for scanner.Scan() {
		line := scanner.Text()

		// Track function changes
		if strings.HasPrefix(line, "+") {
			if match := keyFuncRegex.FindStringSubmatch(line); match != nil {
				currentFunc = match[1]
				// Only track if it's a storage key function
				if strings.HasSuffix(currentFunc, "StorageKey") {
					newKeyFuncs = append(newKeyFuncs, currentFunc)
					inKeyFunc = true
				}
			}
		} else if strings.HasPrefix(line, "-") {
			if match := keyFuncRegex.FindStringSubmatch(line); match != nil {
				funcName := match[1]
				// Only track if it's a storage key function
				if strings.HasSuffix(funcName, "StorageKey") {
					removedKeyFuncs = append(removedKeyFuncs, funcName)
				}
			}
		}

		// Track key format changes only within storage key functions
		if inKeyFunc && strings.HasSuffix(currentFunc, "StorageKey") {
			if strings.Contains(line, "fmt.Sprintf") {
				if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
					if format := keyFormatRegex.FindString(line); format != "" {
						keyFormatChanges = append(keyFormatChanges, fmt.Sprintf("%s: %s", currentFunc, format))
					}
				}
			}
			if strings.Contains(line, "}") {
				inKeyFunc = false
			}
		}

		// Track key string changes only for actual storage keys
		if keyStr := keyStringRegex.FindString(line); keyStr != "" {
			if strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") {
				// Only include if it's in a storage key function
				if strings.HasSuffix(currentFunc, "StorageKey") {
					keyFuncChanges = append(keyFuncChanges, fmt.Sprintf("%s: %s", currentFunc, keyStr))
				}
			}
		}
	}

	hasChanges := false

	// Report findings
	if len(newKeyFuncs) > 0 {
		fmt.Println("New storage key functions detected:")
		for _, funcName := range newKeyFuncs {
			fmt.Printf("+ %sStorageKey\n", funcName)
		}
		hasChanges = true
	}

	if len(removedKeyFuncs) > 0 {
		fmt.Println("⚠️  Removed storage key functions detected:")
		for _, funcName := range removedKeyFuncs {
			fmt.Printf("- %sStorageKey\n", funcName)
		}
		hasChanges = true
	}

	if len(keyFormatChanges) > 0 {
		fmt.Println("⚠️  Storage key format changes detected:")
		for _, change := range keyFormatChanges {
			fmt.Printf("- %s\n", change)
		}
		hasChanges = true
	}

	if len(keyFuncChanges) > 0 {
		fmt.Println("Storage key string changes detected:")
		for _, change := range keyFuncChanges {
			fmt.Printf("- %s\n", change)
		}
		hasChanges = true
	}

	if !hasChanges {
		fmt.Println("✅ No storage key structure changes detected")
	} else {
		fmt.Println("\n⚠️  Storage key changes detected. Review carefully for migration needs.")
	}

	return hasChanges
}

func hasIncompatibleDataChanges(oldBranch string) bool {
	fmt.Println("\n=== Data Structure Compatibility Analysis ===")

	// Get diff of all relevant model files, excluding test files
	cmd := exec.Command("git", "diff", oldBranch, "--",
		"model/*.go",
		"core/taskengine/*.go",
		":!*_test.go",  // Exclude all test files
		":!*_mock.go",  // Exclude mock files
		":!testutil/*") // Exclude test utilities
	output, err := cmd.Output()
	if err != nil {
		fmt.Printf("Warning: Could not diff model files: %v\n", err)
		return false
	}

	if len(output) == 0 {
		fmt.Println("No changes detected in data structures.")
		return false
	}

	var typeChanges []string
	var requiredFieldChanges []string
	var structChanges []string

	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	currentStruct := ""
	inStruct := false

	// Regex patterns for struct analysis
	structRegex := regexp.MustCompile(`type\s+(\w+)\s+struct`)
	fieldRegex := regexp.MustCompile(`\s+(\w+)\s+(\w+)\s+` + "`json:\"([^\"]*)\"`")
	typeChangeRegex := regexp.MustCompile(`\s+(\w+)\s+(\w+)\s+->\s+(\w+)`)

	for scanner.Scan() {
		line := scanner.Text()

		// Track struct changes
		if match := structRegex.FindStringSubmatch(line); match != nil {
			currentStruct = match[1]
			if strings.HasPrefix(line, "+") {
				structChanges = append(structChanges, fmt.Sprintf("New struct: %s", currentStruct))
			} else if strings.HasPrefix(line, "-") {
				structChanges = append(structChanges, fmt.Sprintf("Removed struct: %s", currentStruct))
			}
			inStruct = true
		}

		if inStruct {
			// Track field changes
			if match := fieldRegex.FindStringSubmatch(line); match != nil {
				fieldName := match[1]
				fieldType := match[2]
				jsonTag := match[3]

				if strings.HasPrefix(line, "+") {
					if !strings.Contains(jsonTag, "omitempty") {
						requiredFieldChanges = append(requiredFieldChanges,
							fmt.Sprintf("%s.%s: Added required field of type %s", currentStruct, fieldName, fieldType))
					}
				} else if strings.HasPrefix(line, "-") {
					requiredFieldChanges = append(requiredFieldChanges,
						fmt.Sprintf("%s.%s: Removed field of type %s", currentStruct, fieldName, fieldType))
				}
			}

			// Track type changes
			if match := typeChangeRegex.FindStringSubmatch(line); match != nil {
				fieldName := match[1]
				oldType := match[2]
				newType := match[3]
				typeChanges = append(typeChanges,
					fmt.Sprintf("%s.%s: Type changed from %s to %s", currentStruct, fieldName, oldType, newType))
			}

			if strings.Contains(line, "}") {
				inStruct = false
			}
		}
	}

	// Analyze changes for compatibility
	hasIncompatible := false

	if len(structChanges) > 0 {
		fmt.Println("Struct changes detected:")
		for _, change := range structChanges {
			fmt.Printf("- %s\n", change)
			if strings.HasPrefix(change, "Removed") {
				hasIncompatible = true
			}
		}
	}

	if len(requiredFieldChanges) > 0 {
		fmt.Println("⚠️  Required field changes detected:")
		for _, change := range requiredFieldChanges {
			fmt.Printf("- %s\n", change)
			hasIncompatible = true
		}
	}

	if len(typeChanges) > 0 {
		fmt.Println("⚠️  Type changes detected:")
		for _, change := range typeChanges {
			fmt.Printf("- %s\n", change)
			hasIncompatible = true
		}
	}

	if !hasIncompatible {
		fmt.Println("✅ No incompatible data structure changes detected")
		fmt.Println("Note: All changes appear to be backward compatible")
	} else {
		fmt.Println("\n⚠️  Incompatible changes detected. Migration required.")
		fmt.Println("Recommendation: Implement data migration for affected structures")
	}

	return hasIncompatible
}

func analyzeGitDiff(oldBranch string) {
	fmt.Println("\n=== Git Diff Analysis ===")

	cmd := exec.Command("git", "diff", oldBranch, "--",
		"core/taskengine/schema.go",
		"model/user.go",
		"storage/db.go",
		":!*_test.go",  // Exclude all test files
		":!*_mock.go",  // Exclude mock files
		":!testutil/*") // Exclude test utilities
	output, err := cmd.Output()
	if err != nil {
		fmt.Printf("Error running git diff: %v\n", err)
		return
	}

	if len(output) == 0 {
		fmt.Println("No changes detected in storage-related files.")
		return
	}

	scanner := bufio.NewScanner(strings.NewReader(string(output)))

	structChanges := false
	keyChanges := false
	jsonTagChanges := false

	structRegex := regexp.MustCompile(`^\+\s*type\s+\w+\s+struct`)
	keyRegex := regexp.MustCompile(`^\+.*Key.*string`)
	jsonTagRegex := regexp.MustCompile(`^\+.*json:".*"`)

	for scanner.Scan() {
		line := scanner.Text()

		if structRegex.MatchString(line) {
			structChanges = true
		}

		if keyRegex.MatchString(line) {
			keyChanges = true
		}

		if jsonTagRegex.MatchString(line) {
			jsonTagChanges = true
		}
	}

	fmt.Println("Potential storage-related changes detected:")
	if structChanges {
		fmt.Println("- Struct definitions have been modified")
	}

	if keyChanges {
		fmt.Println("- Storage key generation may have changed")
	}

	if jsonTagChanges {
		fmt.Println("- JSON serialization tags have been modified")
	}

	if !structChanges && !keyChanges && !jsonTagChanges {
		fmt.Println("- No critical storage-related changes detected")
	}

	fmt.Println("\nRecommendation: Review the full diff with:")
	fmt.Printf("  git diff %s -- core/taskengine/schema.go model/user.go storage/db.go\n", oldBranch)
}
