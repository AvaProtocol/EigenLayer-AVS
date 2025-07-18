package taskengine

import (
	"strings"
	"testing"
)

func TestSecurityValidation(t *testing.T) {
	tests := []struct {
		name            string
		expression      string
		shouldBeBlocked bool
	}{
		{
			name:            "Safe property access",
			expression:      "item.age > 18",
			shouldBeBlocked: false,
		},
		{
			name:            "Safe comparison",
			expression:      "trigger.data.minAge <= item.age",
			shouldBeBlocked: false,
		},
		{
			name:            "Dangerous eval",
			expression:      "eval('malicious code')",
			shouldBeBlocked: true,
		},
		{
			name:            "Dangerous Function constructor",
			expression:      "Function('return process.exit(1)')",
			shouldBeBlocked: true,
		},
		{
			name:            "Dangerous setTimeout",
			expression:      "setTimeout('malicious code', 1000)",
			shouldBeBlocked: true,
		},
		{
			name:            "Dangerous global access",
			expression:      "global.process.exit(1)",
			shouldBeBlocked: true,
		},
		{
			name:            "Dangerous require",
			expression:      "require('fs').readFileSync('/etc/passwd')",
			shouldBeBlocked: true,
		},
		{
			name:            "Dangerous constructor access",
			expression:      "item.constructor.constructor('return process.exit(1)')",
			shouldBeBlocked: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test basic security validation
			isDangerous := containsDangerousPatterns(tt.expression)

			if tt.shouldBeBlocked && !isDangerous {
				t.Errorf("Expected expression '%s' to be blocked but it wasn't", tt.expression)
			}

			if !tt.shouldBeBlocked && isDangerous {
				t.Errorf("Expected expression '%s' to be allowed but it was blocked", tt.expression)
			}
		})
	}
}

func TestFilterExpressionSafety(t *testing.T) {
	validator := NewExpressionValidator(DefaultSecurityConfig())

	tests := []struct {
		name            string
		expression      string
		shouldBeBlocked bool
	}{
		{
			name:            "Safe filter expression",
			expression:      "item.age >= 18",
			shouldBeBlocked: false,
		},
		{
			name:            "Dangerous eval in filter",
			expression:      "eval('malicious code')",
			shouldBeBlocked: true,
		},
		{
			name:            "Dangerous Function in filter",
			expression:      "Function('return process.exit(1)')",
			shouldBeBlocked: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sanitizedExpr := validator.SanitizeExpression(tt.expression)
			err := validator.ValidateExpression(sanitizedExpr)
			isBlocked := err != nil

			if tt.shouldBeBlocked && !isBlocked {
				t.Errorf("Expected expression '%s' to be blocked but it was allowed", tt.expression)
			}

			if !tt.shouldBeBlocked && isBlocked {
				t.Errorf("Expected expression '%s' to be allowed but it was blocked: %v", tt.expression, err)
			}
		})
	}
}

func TestExpressionValidation(t *testing.T) {
	validator := NewExpressionValidator(DefaultSecurityConfig())

	tests := []struct {
		name       string
		expression string
		shouldFail bool
	}{
		{
			name:       "Simple property access",
			expression: "item.age",
			shouldFail: false,
		},
		{
			name:       "Numeric comparison",
			expression: "item.age > 18",
			shouldFail: false,
		},
		{
			name:       "Dangerous eval",
			expression: "eval('code')",
			shouldFail: true,
		},
		{
			name:       "Dangerous Function",
			expression: "Function('code')",
			shouldFail: true,
		},
		{
			name:       "Too long expression",
			expression: strings.Repeat("a", 1000),
			shouldFail: true,
		},
		{
			name:       "Nested expressions",
			expression: "{{ nested {{ expression }} }}",
			shouldFail: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validator.ValidateExpression(tt.expression)

			if tt.shouldFail && err == nil {
				t.Errorf("Expected expression '%s' to fail validation but it passed", tt.expression)
			}

			if !tt.shouldFail && err != nil {
				t.Errorf("Expected expression '%s' to pass validation but it failed: %v", tt.expression, err)
			}
		})
	}
}

func TestValidateTemplateExpression(t *testing.T) {
	tests := []struct {
		name        string
		expr        string
		expectValid bool
	}{
		{
			name:        "Safe expression",
			expr:        "data.user.name",
			expectValid: true,
		},
		{
			name:        "Dangerous expression",
			expr:        "eval('malicious code')",
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateTemplateExpression(tt.expr)
			if result.Valid != tt.expectValid {
				t.Errorf("ValidateTemplateExpression() = %v, want %v for expression: %s", result.Valid, tt.expectValid, tt.expr)
			}
		})
	}
}

// TestValidateCodeInjection tests the code injection validation function
func TestValidateCodeInjection(t *testing.T) {
	tests := []struct {
		name        string
		expr        string
		expectValid bool
	}{
		{
			name:        "Safe property access",
			expr:        "test.missing.value",
			expectValid: true,
		},
		{
			name:        "Safe variable reference",
			expr:        "trigger.data.minAge",
			expectValid: true,
		},
		{
			name:        "Safe comparison",
			expr:        "item.age >= 18",
			expectValid: true,
		},
		{
			name:        "Dangerous eval",
			expr:        "eval('malicious code')",
			expectValid: false,
		},
		{
			name:        "Dangerous Function constructor",
			expr:        "Function('return process.exit(1)')",
			expectValid: false,
		},
		{
			name:        "Dangerous setTimeout",
			expr:        "setTimeout('malicious', 1000)",
			expectValid: false,
		},
		{
			name:        "Dangerous global access",
			expr:        "global.process.exit(1)",
			expectValid: false,
		},
		{
			name:        "Dangerous require",
			expr:        "require('fs').readFileSync('/etc/passwd')",
			expectValid: false,
		},
		{
			name:        "Dangerous constructor access",
			expr:        "item.constructor.constructor('malicious')",
			expectValid: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ValidateCodeInjection(tt.expr)
			if result.Valid != tt.expectValid {
				t.Errorf("ValidateCodeInjection() = %v, want %v for expression: %s. Error: %s", result.Valid, tt.expectValid, tt.expr, result.Error)
			}
		})
	}
}

// Helper function to test dangerous patterns
func containsDangerousPatterns(expr string) bool {
	dangerousPatterns := []string{
		"eval", "Function", "setTimeout", "setInterval", "require", "import",
		"export", "global", "process", "constructor",
	}

	for _, pattern := range dangerousPatterns {
		if strings.Contains(expr, pattern) {
			return true
		}
	}

	return false
}
