package taskengine

import (
	"fmt"
	"regexp"
	"strings"
)

// templateVarRegex matches template variables like {{variable.path}}
var templateVarRegex = regexp.MustCompile(`\{\{([^}]+)\}\}`)

// ValidateTemplateVariableResolution checks if a resolved template string contains "undefined" values
// and returns a helpful error message indicating which node dependencies are missing.
//
// This function should be called after preprocessTextWithVariableMapping to validate that
// all template variables were successfully resolved.
//
// Parameters:
//   - resolvedValue: The string after template variable preprocessing
//   - originalValue: The original string before preprocessing (used to extract failed variables)
//   - vm: The VM context (used to check which variables failed to resolve)
//   - contextName: A descriptive name for the context (e.g., "url", "body", "methodParams[0]")
//
// Returns:
//   - error: nil if no undefined values found, otherwise an error with helpful message
func ValidateTemplateVariableResolution(resolvedValue, originalValue string, vm *VM, contextName string) error {
	// Skip validation for immediate execution (RunNodeImmediately) - allow undefined values
	// VMs created for immediate execution have task == nil
	if vm != nil {
		vm.mu.Lock()
		hasTask := vm.task != nil
		vm.mu.Unlock()
		if !hasTask {
			// This is an immediate execution, allow undefined values
			return nil
		}
	}

	// First, check if there are any template variables in the original string
	hasTemplateVars := templateVarRegex.MatchString(originalValue)

	// If there are no template variables, then any "undefined" in the string is legitimate code
	// (e.g., JavaScript "return undefined;" is valid and should not be flagged)
	if !hasTemplateVars {
		return nil // No template variables to validate, allow literal "undefined" in code
	}

	// If there are template variables, check if any of them failed to resolve
	// Extract failed variables from the original template string
	failedVars := extractFailedVariables(originalValue, vm)

	if len(failedVars) == 0 {
		// Template variables exist and all resolved successfully
		// Note: resolvedValue might still contain "undefined" as literal code, but that's OK
		// since we verified no template variables resolved to "undefined"
		return nil
	}

	// Extract the first failed variable name for cleaner error message
	var firstFailedVar string
	for varName := range failedVars {
		firstFailedVar = varName
		break
	}

	// Extract node name from the failed variable (e.g., "get_quote" from "get_quote.data.field")
	parts := strings.Split(firstFailedVar, ".")
	if len(parts) > 0 {
		nodeName := parts[0]
		// If it's a node dependency (not a system variable), provide helpful suggestion
		if !isSystemVariable(nodeName) {
			return fmt.Errorf("could not resolve variable %s in %s. Make sure to run the '%s' node first",
				firstFailedVar, contextName, nodeName)
		}
	}

	// Fallback for system variables or other cases
	return fmt.Errorf("could not resolve variable %s in %s", firstFailedVar, contextName)
}

// isSystemVariable checks if a variable name is a system variable (not a node dependency)
func isSystemVariable(varName string) bool {
	systemVars := []string{"settings", "trigger", "workflowContext", "apContext", "secrets", "env"}
	for _, sysVar := range systemVars {
		if varName == sysVar {
			return true
		}
	}
	return false
}

// ValidateResolvedParams validates an array of resolved parameters for undefined values
// This is useful for validating method parameters after JSON array expansion
func ValidateResolvedParams(resolvedParams []string, originalParams []string, vm *VM, nodeName string) error {
	for i, resolvedParam := range resolvedParams {
		if resolvedParam == "undefined" || strings.Contains(resolvedParam, "undefined") {
			// Get original param for error context
			var originalParam string
			if i < len(originalParams) {
				originalParam = originalParams[i]
			}

			// Use nodeName directly without parameter index for cleaner message
			if err := ValidateTemplateVariableResolution(resolvedParam, originalParam, vm, nodeName); err != nil {
				return err
			}
		}
	}
	return nil
}

// extractFailedVariables extracts template variables from a parameter string and identifies which ones resolved to "undefined"
func extractFailedVariables(originalParam string, vm *VM) map[string]string {
	failedVars := make(map[string]string)

	// Find all template variables: {{variable.path}}
	matches := templateVarRegex.FindAllStringSubmatch(originalParam, -1)

	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		varExpr := strings.TrimSpace(match[1])
		varTemplate := match[0] // Full template including {{ }}

		// Resolve just this variable to check if it becomes "undefined"
		resolved := vm.preprocessTextWithVariableMapping(varTemplate)

		if resolved == "undefined" {
			failedVars[varExpr] = "undefined"
		}
	}

	return failedVars
}
