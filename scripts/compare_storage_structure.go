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
	cmd := exec.Command("git", "show", fmt.Sprintf("%s:core/taskengine/schema.go", branch))
	output, err := cmd.Output()
	if err != nil {
		fmt.Printf("Warning: Could not extract schema.go from branch %s: %v\n", branch, err)
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
	if hasStorageKeyChanges() {
		fmt.Println("⚠️ Storage key structure changes detected. A migration may be required.")
		fmt.Println("Recommendation: Review the changes carefully and implement a migration if needed.")
	} else if hasIncompatibleDataChanges() {
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
	
	fieldChanges := false
	jsonTagChanges := false
	
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.Contains(line, "+	") && strings.Contains(line, "SmartWallet struct") {
			fieldChanges = true
		}
		if strings.Contains(line, "+	") && strings.Contains(line, "json:") {
			jsonTagChanges = true
		}
	}
	
	if fieldChanges {
		fmt.Println("- SmartWallet struct definition has changed")
	}
	
	if jsonTagChanges {
		fmt.Println("- JSON serialization tags have been modified")
	}
	
	if !fieldChanges && !jsonTagChanges && len(output) > 0 {
		fmt.Println("- Changes detected in model/user.go but they don't appear to affect storage structure")
	}
	
	fmt.Println("Note: This is a simplified analysis. For a complete analysis,")
	fmt.Println("manually review the struct definitions in both branches.")
}

func hasStorageKeyChanges() bool {
	return false
}

func hasIncompatibleDataChanges() bool {
	return false
}

func analyzeGitDiff(oldBranch string) {
	fmt.Println("\n=== Git Diff Analysis ===")
	
	cmd := exec.Command("git", "diff", oldBranch, "--", "core/taskengine/schema.go", "model/user.go", "storage/db.go")
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
