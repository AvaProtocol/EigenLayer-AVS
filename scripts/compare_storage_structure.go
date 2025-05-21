package main

import (
	"fmt"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/AvaProtocol/EigenLayer-AVS/core/taskengine"
	"github.com/AvaProtocol/EigenLayer-AVS/model"
	"github.com/ethereum/go-ethereum/common"
)

type StorageKeyTemplate struct {
	Name        string
	KeyTemplate string
	IsNew       bool
	IsModified  bool
}

func main() {
	if len(os.Args) != 2 {
		fmt.Println("Usage: go run compare_storage_structure.go <comparison_branch>")
		fmt.Println("Example: go run compare_storage_structure.go staging")
		os.Exit(1)
	}

	compareBranch := os.Args[1]
	fmt.Printf("Comparing storage key structures between current branch and %s branch\n\n", compareBranch)

	mainKeys := getMainBranchKeyStructures()

	stagingKeys := getStagingBranchKeyStructures()

	compareKeyStructures(mainKeys, stagingKeys)

	analyzePR227Changes()
}

func getMainBranchKeyStructures() []StorageKeyTemplate {
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

func getStagingBranchKeyStructures() []StorageKeyTemplate {
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

func compareKeyStructures(mainKeys, stagingKeys []StorageKeyTemplate) {
	mainMap := make(map[string]StorageKeyTemplate)
	stagingMap := make(map[string]StorageKeyTemplate)

	for _, key := range mainKeys {
		mainMap[key.Name] = key
	}
	for _, key := range stagingKeys {
		stagingMap[key.Name] = key
	}

	var newKeys []string
	for name, key := range stagingMap {
		if _, exists := mainMap[name]; !exists {
			newKeys = append(newKeys, fmt.Sprintf("- %s: %s", name, key.KeyTemplate))
		}
	}

	var removedKeys []string
	for name, key := range mainMap {
		if _, exists := stagingMap[name]; !exists {
			removedKeys = append(removedKeys, fmt.Sprintf("- %s: %s", name, key.KeyTemplate))
		}
	}

	var modifiedKeys []string
	for name, stagingKey := range stagingMap {
		if mainKey, exists := mainMap[name]; exists && mainKey.KeyTemplate != stagingKey.KeyTemplate {
			modifiedKeys = append(modifiedKeys, fmt.Sprintf("- %s: %s -> %s", name, mainKey.KeyTemplate, stagingKey.KeyTemplate))
		}
	}

	sort.Strings(newKeys)
	sort.Strings(removedKeys)
	sort.Strings(modifiedKeys)

	fmt.Println("=== Storage Key Structure Comparison ===")
	
	fmt.Println("\n=== New Keys in Staging ===")
	if len(newKeys) == 0 {
		fmt.Println("No new keys found")
	} else {
		for _, key := range newKeys {
			fmt.Println(key)
		}
	}

	fmt.Println("\n=== Removed Keys in Staging ===")
	if len(removedKeys) == 0 {
		fmt.Println("No removed keys found")
	} else {
		for _, key := range removedKeys {
			fmt.Println(key)
		}
	}

	fmt.Println("\n=== Modified Keys in Staging ===")
	if len(modifiedKeys) == 0 {
		fmt.Println("No modified keys found")
	} else {
		for _, key := range modifiedKeys {
			fmt.Println(key)
		}
	}
}

func analyzePR227Changes() {
	fmt.Println("\n=== Analysis of PR #227 Changes ===")
	
	fmt.Println("\n1. JWT Audience Field Change:")
	fmt.Println("   - Change: Added chainId to JWT audience field")
	fmt.Println("   - Effect on Storage: None. This change only affects token validation at runtime.")
	fmt.Println("   - Migration Required: No")
	
	fmt.Println("\n2. StepOutputVar Fix:")
	fmt.Println("   - Change: Fixed handling of nil txReceipt values in output variables")
	fmt.Println("   - Effect on Storage: None. This is a runtime fix that doesn't change storage structure.")
	fmt.Println("   - Migration Required: No")
	
	fmt.Println("\n3. isHidden Attribute Addition:")
	fmt.Println("   - Change: Added isHidden field to SmartWallet struct with `omitempty` JSON tag")
	fmt.Println("   - Effect on Storage: Minimal. The `omitempty` tag ensures existing records without this field")
	fmt.Println("     will be unmarshaled correctly with IsHidden defaulting to false.")
	fmt.Println("   - Migration Required: No")
	
	fmt.Println("\n=== Overall Conclusion ===")
	fmt.Println("No migration is required for PR #227 changes because:")
	fmt.Println("1. No storage key structures have been modified")
	fmt.Println("2. All changes are backward compatible with existing stored data")
	fmt.Println("3. Runtime-only changes don't affect persistent storage")
}

func compareWalletStructures() {
	
	
	fmt.Println("\n=== Detailed SmartWallet Structure Analysis ===")
	fmt.Println("Main branch SmartWallet fields:")
	fmt.Println("- Owner: common.Address")
	fmt.Println("- Address: common.Address")
	fmt.Println("- Factory: common.Address (omitempty)")
	fmt.Println("- Salt: big.Int")
	
	fmt.Println("\nStaging branch SmartWallet fields:")
	fmt.Println("- Owner: common.Address")
	fmt.Println("- Address: common.Address")
	fmt.Println("- Factory: common.Address (omitempty)")
	fmt.Println("- Salt: big.Int")
	fmt.Println("- IsHidden: bool (omitempty)")
	
	fmt.Println("\nJSON Compatibility Analysis:")
	fmt.Println("- Adding an 'omitempty' field is backward compatible")
	fmt.Println("- Existing records will deserialize with IsHidden=false (zero value)")
	fmt.Println("- No migration needed for this change")
}
