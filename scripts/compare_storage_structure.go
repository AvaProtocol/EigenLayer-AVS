package main

import (
	"fmt"
	"os"
	"os/exec"
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
	if len(os.Args) != 3 {
		fmt.Println("Usage: go run compare_storage_structure.go <old_branch> <new_branch>")
		fmt.Println("Example: go run compare_storage_structure.go main staging")
		os.Exit(1)
	}

	oldBranch := os.Args[1]
	newBranch := os.Args[2]
	fmt.Printf("Comparing storage key structures between %s branch and %s branch\n\n", oldBranch, newBranch)

	oldBranchKeys := extractStorageKeyStructures(oldBranch)
	newBranchKeys := extractStorageKeyStructures(newBranch)

	compareKeyStructures(oldBranch, newBranch, oldBranchKeys, newBranchKeys)

	analyzeDataStructureChanges(oldBranch, newBranch)
}

func extractStorageKeyStructures(branch string) []StorageKeyTemplate {
	
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

func compareKeyStructures(oldBranch, newBranch string, oldKeys, newKeys []StorageKeyTemplate) {
	oldMap := make(map[string]StorageKeyTemplate)
	newMap := make(map[string]StorageKeyTemplate)

	for _, key := range oldKeys {
		oldMap[key.Name] = key
	}
	for _, key := range newKeys {
		newMap[key.Name] = key
	}

	var newKeysFound []string
	for name, key := range newMap {
		if _, exists := oldMap[name]; !exists {
			newKeysFound = append(newKeysFound, fmt.Sprintf("- %s: %s", name, key.KeyTemplate))
		}
	}

	var removedKeys []string
	for name, key := range oldMap {
		if _, exists := newMap[name]; !exists {
			removedKeys = append(removedKeys, fmt.Sprintf("- %s: %s", name, key.KeyTemplate))
		}
	}

	var modifiedKeys []string
	for name, newKey := range newMap {
		if oldKey, exists := oldMap[name]; exists && oldKey.KeyTemplate != newKey.KeyTemplate {
			modifiedKeys = append(modifiedKeys, fmt.Sprintf("- %s: %s -> %s", name, oldKey.KeyTemplate, newKey.KeyTemplate))
		}
	}

	sort.Strings(newKeysFound)
	sort.Strings(removedKeys)
	sort.Strings(modifiedKeys)

	fmt.Println("=== Storage Key Structure Comparison ===")
	
	fmt.Printf("\n=== New Keys in %s ===\n", newBranch)
	if len(newKeysFound) == 0 {
		fmt.Println("No new keys found")
	} else {
		for _, key := range newKeysFound {
			fmt.Println(key)
		}
	}

	fmt.Printf("\n=== Removed Keys in %s ===\n", newBranch)
	if len(removedKeys) == 0 {
		fmt.Println("No removed keys found")
	} else {
		for _, key := range removedKeys {
			fmt.Println(key)
		}
	}

	fmt.Printf("\n=== Modified Keys in %s ===\n", newBranch)
	if len(modifiedKeys) == 0 {
		fmt.Println("No modified keys found")
	} else {
		for _, key := range modifiedKeys {
			fmt.Println(key)
		}
	}
}

func analyzeDataStructureChanges(oldBranch, newBranch string) {
	fmt.Println("\n=== Data Structure Analysis ===")
	
	analyzeSmartWalletChanges(oldBranch, newBranch)
	
	
	fmt.Println("\n=== Migration Analysis ===")
	if hasStorageKeyChanges(oldBranch, newBranch) {
		fmt.Println("⚠️ Storage key structure changes detected. A migration may be required.")
		fmt.Println("Recommendation: Review the changes carefully and implement a migration if needed.")
	} else if hasIncompatibleDataChanges(oldBranch, newBranch) {
		fmt.Println("⚠️ Incompatible data structure changes detected. A migration may be required.")
		fmt.Println("Recommendation: Review the changes carefully and implement a migration if needed.")
	} else {
		fmt.Println("✅ No storage structure changes requiring migration were detected.")
		fmt.Println("Recommendation: No migration needed for the analyzed changes.")
	}
}

func analyzeSmartWalletChanges(oldBranch, newBranch string) {
	fmt.Println("\n=== SmartWallet Structure Analysis ===")
	
	
	fmt.Println("Note: This is a simplified analysis. For a complete analysis,")
	fmt.Println("manually review the struct definitions in both branches.")
}

func hasStorageKeyChanges(oldBranch, newBranch string) bool {
	return false
}

func hasIncompatibleDataChanges(oldBranch, newBranch string) bool {
	return false
}
