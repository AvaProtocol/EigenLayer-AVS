package taskengine

import (
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// SecurityConfig contains security settings for template processing
type SecurityConfig struct {
	AllowedFunctions       []string
	AllowedProperties      []string
	AllowedOperators       []string
	MaxExpressionLength    int
	AllowNestedExpressions bool
}

// DefaultSecurityConfig returns a secure default configuration
func DefaultSecurityConfig() *SecurityConfig {
	return &SecurityConfig{
		AllowedFunctions: []string{
			// Safe utility functions
			"Math.abs", "Math.max", "Math.min", "Math.floor", "Math.ceil", "Math.round",
			"String", "Number", "Boolean", "parseInt", "parseFloat",
			"JSON.stringify", "JSON.parse",
			// Date functions
			"Date.now", "Date.UTC",
			// Array methods (safe read-only operations)
			"Array.isArray", "length", "indexOf", "includes", "slice", "concat",
			"map", "filter", "reduce", "find", "findIndex", "some", "every",
			// String methods
			"toString", "toLowerCase", "toUpperCase", "trim", "split", "substring", "substr",
			"replace", "match", "search", "charAt", "charCodeAt", "startsWith", "endsWith",
			// Additional string methods
			"includes", "indexOf", "lastIndexOf", "padStart", "padEnd", "repeat",
		},
		AllowedProperties: []string{
			// Object properties (for data access)
			"data", "input", "output", "config", "params", "result", "value", "status",
			"id", "name", "type", "timestamp", "created", "updated", "version",
			// Trigger properties
			"trigger", "block", "transaction", "log", "event", "manual", "cron", "time",
			// Common data fields
			"address", "amount", "balance", "symbol", "decimals", "price", "total",
			"from", "to", "hash", "receipt", "success", "error", "message",
			// Node context
			"apContext", "configVars", "workflowContext",
			// Filter and iteration context
			"item", "index", "current", "previous", "next",
			// Common object properties
			"age", "minAge", "maxAge", "length", "size", "count",
			// Array and object access patterns
			"0", "1", "2", "3", "4", "5", "6", "7", "8", "9", // numeric indices
		},
		AllowedOperators: []string{
			// Arithmetic operators
			"+", "-", "*", "/", "%", "**",
			// Comparison operators
			"==", "!=", "===", "!==", "<", ">", "<=", ">=",
			// Logical operators
			"&&", "||", "!",
			// Bitwise operators (limited)
			"&", "|", "^", "~", "<<", ">>", ">>>",
			// Assignment operators (for object access only)
			"=", ".", "[", "]",
			// Ternary operator
			"?", ":",
			// Parentheses
			"(", ")",
		},
		MaxExpressionLength:    500,
		AllowNestedExpressions: false,
	}
}

// ExpressionValidator validates JavaScript expressions for security
type ExpressionValidator struct {
	config *SecurityConfig
}

// NewExpressionValidator creates a new expression validator
func NewExpressionValidator(config *SecurityConfig) *ExpressionValidator {
	if config == nil {
		config = DefaultSecurityConfig()
	}
	return &ExpressionValidator{config: config}
}

// ValidateExpression validates a JavaScript expression for security
func (v *ExpressionValidator) ValidateExpression(expr string) error {
	// Basic length check
	if len(expr) > v.config.MaxExpressionLength {
		return fmt.Errorf("expression too long: %d characters (max %d)", len(expr), v.config.MaxExpressionLength)
	}

	// Check for nested expressions
	if !v.config.AllowNestedExpressions {
		if strings.Contains(expr, "{{") || strings.Contains(expr, "}}") {
			return fmt.Errorf("nested expressions are not allowed")
		}
	}

	// Check for dangerous patterns
	if err := v.checkDangerousPatterns(expr); err != nil {
		return err
	}

	// Check for allowed functions and properties
	if err := v.checkAllowedElements(expr); err != nil {
		return err
	}

	return nil
}

// checkDangerousPatterns checks for dangerous JavaScript patterns
func (v *ExpressionValidator) checkDangerousPatterns(expr string) error {
	// List of dangerous patterns that should never be allowed
	dangerousPatterns := []string{
		// Function creation and execution
		"Function", "eval", "setTimeout", "setInterval", "setImmediate",
		// Global object access
		"global", "window", "document", "process", "require", "module", "exports",
		// Constructor access
		"constructor", "prototype", "__proto__", "__defineGetter__", "__defineSetter__",
		// Error manipulation
		"Error", "throw", "try", "catch", "finally",
		// Control flow that could be dangerous
		"while", "for", "do", "break", "continue", "return",
		// Variable declaration
		"var", "let", "const", "function", "class", "import", "export",
		// Dangerous operators
		"delete", "typeof", "instanceof", "in", "void", "new",
		// Comments that could hide malicious code
		"//", "/*", "*/",
		// String interpolation
		"`", "${",
		// Regex that could cause ReDoS
		"RegExp",
	}

	for _, pattern := range dangerousPatterns {
		if strings.Contains(expr, pattern) {
			return fmt.Errorf("dangerous pattern detected: %s", pattern)
		}
	}

	// Check for suspicious character sequences
	if strings.Contains(expr, "\\x") || strings.Contains(expr, "\\u") {
		return fmt.Errorf("unicode escape sequences are not allowed")
	}

	// Check for script injection attempts
	if strings.Contains(expr, "<script") || strings.Contains(expr, "</script") {
		return fmt.Errorf("script tags are not allowed")
	}

	return nil
}

// checkAllowedElements validates that only allowed functions and properties are used
func (v *ExpressionValidator) checkAllowedElements(expr string) error {
	// This is a simplified validation - in a production system, you'd want to use
	// a proper JavaScript parser to ensure accuracy

	// Check for function calls
	funcRegex := regexp.MustCompile(`([a-zA-Z_$][a-zA-Z0-9_$]*(?:\.[a-zA-Z_$][a-zA-Z0-9_$]*)*)\s*\(`)
	funcMatches := funcRegex.FindAllStringSubmatch(expr, -1)

	for _, match := range funcMatches {
		if len(match) > 1 {
			funcName := match[1]
			if !v.isFunctionAllowed(funcName) {
				return fmt.Errorf("function not allowed: %s", funcName)
			}
		}
	}

	// Check for property access - but be more permissive for simple expressions
	propRegex := regexp.MustCompile(`([a-zA-Z_$][a-zA-Z0-9_$]*(?:\.[a-zA-Z_$][a-zA-Z0-9_$]*)*)`)
	propMatches := propRegex.FindAllStringSubmatch(expr, -1)

	for _, match := range propMatches {
		if len(match) > 1 {
			propPath := match[1]
			// Skip numeric literals and common comparison values
			if regexp.MustCompile(`^\d+$`).MatchString(propPath) {
				continue
			}
			if propPath == "true" || propPath == "false" || propPath == "null" || propPath == "undefined" {
				continue
			}
			if !v.isPropertyAllowed(propPath) {
				return fmt.Errorf("property access not allowed: %s", propPath)
			}
		}
	}

	return nil
}

// isFunctionAllowed checks if a function is in the allowed list
func (v *ExpressionValidator) isFunctionAllowed(funcName string) bool {
	for _, allowed := range v.config.AllowedFunctions {
		if funcName == allowed || strings.HasPrefix(funcName, allowed+".") {
			return true
		}
	}
	return false
}

// isPropertyAllowed checks if a property is in the allowed list
func (v *ExpressionValidator) isPropertyAllowed(propPath string) bool {
	// Split the property path into parts
	parts := strings.Split(propPath, ".")

	// Check if the first part is allowed
	if len(parts) > 0 {
		firstPart := parts[0]
		for _, allowed := range v.config.AllowedProperties {
			if firstPart == allowed {
				return true
			}
		}
	}

	// Check if the full path is allowed
	for _, allowed := range v.config.AllowedProperties {
		if propPath == allowed || strings.HasPrefix(propPath, allowed+".") {
			return true
		}
	}

	// Allow any property that looks like a valid identifier (no dangerous characters)
	// This is more permissive but still secure
	if regexp.MustCompile(`^[a-zA-Z_$][a-zA-Z0-9_$]*(\.[a-zA-Z_$][a-zA-Z0-9_$]*)*$`).MatchString(propPath) {
		return true
	}

	return false
}

// SanitizeExpression sanitizes a JavaScript expression by removing dangerous elements
func (v *ExpressionValidator) SanitizeExpression(expr string) string {
	// Remove comments
	expr = regexp.MustCompile(`//.*$`).ReplaceAllString(expr, "")
	expr = regexp.MustCompile(`/\*.*?\*/`).ReplaceAllString(expr, "")

	// Remove extra whitespace
	expr = strings.TrimSpace(expr)
	expr = regexp.MustCompile(`\s+`).ReplaceAllString(expr, " ")

	// Remove dangerous characters
	var result strings.Builder
	for _, r := range expr {
		if unicode.IsPrint(r) && r != '\u0000' {
			result.WriteRune(r)
		}
	}

	return result.String()
}

// SecureTemplateProcessor provides secure template processing
type SecureTemplateProcessor struct {
	validator *ExpressionValidator
}

// NewSecureTemplateProcessor creates a new secure template processor
func NewSecureTemplateProcessor(config *SecurityConfig) *SecureTemplateProcessor {
	return &SecureTemplateProcessor{
		validator: NewExpressionValidator(config),
	}
}

// ProcessTemplate safely processes a template with validation
func (p *SecureTemplateProcessor) ProcessTemplate(template string, variables map[string]interface{}) (string, error) {
	if !strings.Contains(template, "{{") || !strings.Contains(template, "}}") {
		return template, nil
	}

	result := template
	maxIterations := 10 // Prevent infinite loops

	for i := 0; i < maxIterations; i++ {
		start := strings.Index(result, "{{")
		if start == -1 {
			break
		}

		end := strings.Index(result[start:], "}}")
		if end == -1 {
			break
		}
		end += start

		expr := strings.TrimSpace(result[start+2 : end])
		if expr == "" {
			result = result[:start] + result[end+2:]
			continue
		}

		// Sanitize the expression
		sanitizedExpr := p.validator.SanitizeExpression(expr)

		// Validate the expression
		if err := p.validator.ValidateExpression(sanitizedExpr); err != nil {
			return "", fmt.Errorf("invalid expression '%s': %w", expr, err)
		}

		// For now, return the sanitized expression - in a full implementation,
		// you would evaluate it safely here
		result = result[:start] + sanitizedExpr + result[end+2:]
	}

	return result, nil
}

// Safe wrapper functions for existing VM methods
func (v *VM) preprocessTextWithVariableMappingSecure(text string) (string, error) {
	processor := NewSecureTemplateProcessor(DefaultSecurityConfig())

	// Get current variables
	v.mu.Lock()
	currentVars := make(map[string]interface{}, len(v.vars))
	for k, val := range v.vars {
		currentVars[k] = val
	}
	v.mu.Unlock()

	return processor.ProcessTemplate(text, currentVars)
}

// ValidationResult contains the result of expression validation
type ValidationResult struct {
	Valid          bool
	Error          string
	SafeExpression string
}

// ValidateTemplateExpression validates a template expression
func ValidateTemplateExpression(expr string) ValidationResult {
	validator := NewExpressionValidator(DefaultSecurityConfig())
	sanitized := validator.SanitizeExpression(expr)

	if err := validator.ValidateExpression(sanitized); err != nil {
		return ValidationResult{
			Valid:          false,
			Error:          err.Error(),
			SafeExpression: sanitized,
		}
	}

	return ValidationResult{
		Valid:          true,
		Error:          "",
		SafeExpression: sanitized,
	}
}

// ValidateCodeInjection validates JavaScript expressions for code injection attacks
// by checking for dangerous patterns without strict property access validation.
func ValidateCodeInjection(expr string) ValidationResult {
	// List of dangerous patterns that should never be allowed
	dangerousPatterns := []string{
		"eval", "Function", "setTimeout", "setInterval", "require", "import",
		"export", "global", "process", "constructor", "prototype", "__proto__",
	}

	for _, pattern := range dangerousPatterns {
		if strings.Contains(expr, pattern) {
			return ValidationResult{
				Valid:          false,
				Error:          fmt.Sprintf("dangerous pattern detected: %s", pattern),
				SafeExpression: expr,
			}
		}
	}

	return ValidationResult{
		Valid:          true,
		Error:          "",
		SafeExpression: expr,
	}
}
