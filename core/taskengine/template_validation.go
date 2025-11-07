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
	// Check if the resolved value contains "undefined"
	if resolvedValue != "undefined" && !strings.Contains(resolvedValue, "undefined") {
		return nil // All template variables resolved successfully
	}

	// Extract failed variables from the original template string
	failedVars := extractFailedVariables(originalValue, vm)

	if len(failedVars) == 0 {
		// No specific failed variables found, but "undefined" is present
		return fmt.Errorf("%s contains 'undefined': %s", contextName, resolvedValue)
	}

	// Build list of failed variable names
	var failedVarStrs []string
	for varName, varValue := range failedVars {
		failedVarStrs = append(failedVarStrs, fmt.Sprintf("%s=%s", varName, varValue))
	}

	// Extract node names from failed variables (e.g., "get_quote" from "get_quote.data.field")
	var missingNodeNames []string
	for varName := range failedVars {
		parts := strings.Split(varName, ".")
		if len(parts) > 0 {
			nodeName := parts[0]
			// Skip system variables
			if !isSystemVariable(nodeName) {
				missingNodeNames = append(missingNodeNames, nodeName)
			}
		}
	}

	// Build error message
	if len(missingNodeNames) > 0 {
		return fmt.Errorf("could not resolve template variable in %s: %s. Make sure to run the '%s' node first and include its output data in inputVariables",
			contextName, strings.Join(failedVarStrs, ", "), missingNodeNames[0])
	}

	return fmt.Errorf("could not resolve template variable in %s: %s", contextName, strings.Join(failedVarStrs, ", "))
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

			contextName := fmt.Sprintf("parameter %d in %s", i, nodeName)
			if err := ValidateTemplateVariableResolution(resolvedParam, originalParam, vm, contextName); err != nil {
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
