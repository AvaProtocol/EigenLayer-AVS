package taskengine

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFormatAsJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{
			name:     "nil value",
			input:    nil,
			expected: "null",
		},
		{
			name:     "string value",
			input:    "hello world",
			expected: `"hello world"`,
		},
		{
			name:     "integer value",
			input:    42,
			expected: "42",
		},
		{
			name:     "boolean value",
			input:    true,
			expected: "true",
		},
		{
			name:     "valid JSON string",
			input:    `{"key":"value"}`,
			expected: `{"key":"value"}`,
		},
		{
			name: "simple map",
			input: map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer token123",
			},
			expected: `{"Authorization":"Bearer token123","Content-Type":"application/json"}`,
		},
		{
			name: "http.Header map",
			input: http.Header{
				"Content-Type": []string{"application/json"},
				"X-Request-Id": []string{"abc123"},
			},
			expected: `{"Content-Type":["application/json"],"X-Request-Id":["abc123"]}`,
		},
		{
			name: "JSON with HTML characters (no escaping)",
			input: map[string]interface{}{
				"html":       "<div>Test & \"quotes\"</div>",
				"comparison": "5 > 3 && 2 < 4",
			},
			expected: `{"comparison":"5 > 3 && 2 < 4","html":"<div>Test & \"quotes\"</div>"}`,
		},
		{
			name: "nested structure",
			input: map[string]interface{}{
				"user": map[string]interface{}{
					"name": "John Doe",
					"age":  30,
				},
				"active": true,
			},
			expected: `{"active":true,"user":{"age":30,"name":"John Doe"}}`,
		},
		{
			name:     "array",
			input:    []string{"one", "two", "three"},
			expected: `["one","two","three"]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatAsJSON(tt.input)

			// For map types, we can't guarantee key order, so we need to parse and compare
			if _, ok := tt.input.(map[string]string); ok {
				// Parse both as JSON and compare
				assert.JSONEq(t, tt.expected, result, "JSON should be equivalent")
			} else if _, ok := tt.input.(http.Header); ok {
				// For http.Header, also compare as JSON
				assert.JSONEq(t, tt.expected, result, "JSON should be equivalent")
			} else if m, ok := tt.input.(map[string]interface{}); ok && len(m) > 1 {
				// For maps with multiple keys, compare as JSON
				assert.JSONEq(t, tt.expected, result, "JSON should be equivalent")
			} else {
				// For simple types and single-key maps, exact string match
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestFormatAsJSON_NoHTMLEscaping(t *testing.T) {
	// This test specifically verifies that HTML characters are NOT escaped
	input := map[string]interface{}{
		"html_content": "<html><body><h1>Title</h1></body></html>",
		"comparison":   "x > 5 && y < 10",
		"email":        "user@example.com",
	}

	result := FormatAsJSON(input)

	// Verify that < > & are NOT escaped
	assert.Contains(t, result, "<html>", "< should not be escaped")
	assert.Contains(t, result, "</html>", "> should not be escaped")
	assert.Contains(t, result, "&&", "& should not be escaped")
	assert.Contains(t, result, "@", "@ should not be escaped")

	// Verify that these escaped forms do NOT appear
	assert.NotContains(t, result, `\u003c`, "< should not be unicode-escaped")
	assert.NotContains(t, result, `\u003e`, "> should not be unicode-escaped")
	assert.NotContains(t, result, `\u0026`, "& should not be unicode-escaped")
}

func TestFormatAsJSON_RequestBodyScenario(t *testing.T) {
	// Simulate a real SendGrid request body
	requestBody := `{"from":{"email":"sender@example.com"},"personalizations":[{"to":[{"email":"recipient@example.com"}],"subject":"Test Subject","dynamic_template_data":{"html":"<div><h1>Title</h1><p>Body with & special chars</p></div>","text":"Plain text"}}],"template_id":"d-abc123"}`

	result := FormatAsJSON(requestBody)

	// Since input is already valid JSON, it should be returned as-is
	assert.Equal(t, requestBody, result)

	// Verify no HTML escaping occurred
	assert.Contains(t, result, "<div>")
	assert.Contains(t, result, "</div>")
	assert.Contains(t, result, "&")
}

func TestFormatAsJSON_ResponseHeaderScenario(t *testing.T) {
	// Simulate real HTTP response headers
	headers := http.Header{
		"Content-Type":   []string{"application/json"},
		"Content-Length": []string{"1024"},
		"X-Message-Id":   []string{"abc-123-def"},
		"Date":           []string{"Mon, 03 Nov 2025 17:37:01 GMT"},
	}

	result := FormatAsJSON(headers)

	// Should be valid JSON
	assert.True(t, result[0] == '{', "Should start with {")
	assert.True(t, result[len(result)-1] == '}', "Should end with }")

	// Should contain all headers as JSON
	assert.Contains(t, result, `"Content-Type"`)
	assert.Contains(t, result, `"application/json"`)
	assert.Contains(t, result, `"X-Message-Id"`)
	assert.Contains(t, result, `"abc-123-def"`)

	// Should NOT be in Go map format
	assert.NotContains(t, result, "map[", "Should not contain Go map syntax")
}

func TestFormatAsJSON_SendGridSummaryInjectionScenario(t *testing.T) {
	// This test reproduces the exact scenario from the user's log where HTML-escaped
	// characters were appearing in the request body. It simulates what happens when
	// we inject workflow summaries into SendGrid payloads.

	// Simulate a SendGrid payload with dynamic template data containing HTML
	sendGridPayload := map[string]interface{}{
		"from": map[string]string{
			"email": "product@avaprotocol.org",
		},
		"personalizations": []interface{}{
			map[string]interface{}{
				"to": []interface{}{
					map[string]string{"email": "dev@avaprotocol.org"},
				},
				"subject": "Test template: succeeded (3 out of 7 steps)",
				"dynamic_template_data": map[string]interface{}{
					"analysisHtml":      "<div style=\"font-weight:600; margin:8px 0 4px\">Workflow Summary</div>\n<p style=\"margin:0 0 8px\">The workflow did not fully execute. Some nodes were skipped due to branching conditions. No on-chain transactions executed.</p>",
					"branchSummaryHtml": "<div style=\"font-weight:600; margin:8px 0 4px\">Branch 'branch1': selected Else condition</div>\n<ul style=\"margin:0 0 12px 20px; padding:0\">\n<li>If → <code>{{balance1.data.find(token => token?.tokenAddress?.toLowerCase() === settings.uniswapv3_pool.token1.id.toLowerCase()).balance > Number(settings.amount)}}</code></li>\n<li>Else</li>\n</ul>",
					"skippedNodes":      []string{"run_swap", "email_report_success", "get_quote", "approve_token1"},
					"runner":            "0xeCb88a770e1b2Ba303D0dC3B1c6F239fAB014bAE",
					"eoaAddress":        "0xc60e…C788",
					"preheader":         "The workflow did not fully execute. Some nodes were skipped due to branching conditions.",
					"subject":           "Test template: succeeded (3 out of 7 steps)",
				},
			},
		},
		"template_id": "d-3b4b885af0fc45ad822024ebc72f169c",
		"reply_to": map[string]string{
			"email": "chris@avaprotocol.org",
		},
	}

	// Format using FormatAsJSON (this is what line 780 now does)
	result := FormatAsJSON(sendGridPayload)

	// Critical assertions: HTML characters should NOT be escaped
	// Note: Quotes inside strings will be escaped as \" which is correct for JSON
	assert.Contains(t, result, `<div style=`,
		"Should contain unescaped <div> tags")
	assert.Contains(t, result, `Workflow Summary</div>`,
		"Should contain unescaped closing </div>")
	assert.Contains(t, result, `<p style=`,
		"Should contain unescaped <p> tags")
	assert.Contains(t, result, `<code>`,
		"Should contain unescaped <code> tags")
	assert.Contains(t, result, `</li>`,
		"Should contain unescaped closing tags")
	assert.Contains(t, result, `token => token`,
		"Should contain unescaped arrow function =>")
	assert.Contains(t, result, `balance > Number`,
		"Should contain unescaped > comparison operator")

	// These escaped forms should NOT appear
	assert.NotContains(t, result, `\u003cdiv`,
		"Should NOT contain \\u003c (escaped <)")
	assert.NotContains(t, result, `\u003e`,
		"Should NOT contain \\u003e (escaped >)")
	assert.NotContains(t, result, `\u0026`,
		"Should NOT contain \\u0026 (escaped &)")
	assert.NotContains(t, result, `\u003c/div\u003e`,
		"Should NOT contain escaped closing tags")

	// Verify it's still valid JSON
	var decoded map[string]interface{}
	err := json.Unmarshal([]byte(result), &decoded)
	assert.NoError(t, err, "Result should be valid JSON")

	// Verify the structure is preserved
	assert.Contains(t, decoded, "personalizations", "Should contain personalizations")
	assert.Contains(t, decoded, "template_id", "Should contain template_id")
}

func TestFormatAsJSON_CompareWithStandardMarshal(t *testing.T) {
	// This test explicitly compares FormatAsJSON with standard json.Marshal
	// to demonstrate the difference in HTML escaping behavior

	data := map[string]interface{}{
		"html":       "<div>Content & \"quotes\"</div>",
		"script":     "<script>alert('xss')</script>",
		"comparison": "5 > 3 && x => y",
	}

	// Standard json.Marshal (with HTML escaping - the OLD way)
	standardBytes, err := json.Marshal(data)
	assert.NoError(t, err)
	standardResult := string(standardBytes)

	// FormatAsJSON (without HTML escaping - the NEW way)
	formatResult := FormatAsJSON(data)

	// Standard Marshal SHOULD escape HTML characters
	assert.Contains(t, standardResult, `\u003cdiv\u003e`, "Standard marshal should escape <div>")
	assert.Contains(t, standardResult, `\u0026`, "Standard marshal should escape &")
	assert.Contains(t, standardResult, `\u003e`, "Standard marshal should escape >")

	// FormatAsJSON should NOT escape HTML characters
	assert.Contains(t, formatResult, `<div>`, "FormatAsJSON should NOT escape <div>")
	assert.Contains(t, formatResult, `&`, "FormatAsJSON should NOT escape &")
	assert.Contains(t, formatResult, `>`, "FormatAsJSON should NOT escape >")
	assert.Contains(t, formatResult, `=>`, "FormatAsJSON should NOT escape =>")

	// Both should still be valid JSON
	var decodedStandard, decodedFormat map[string]interface{}
	assert.NoError(t, json.Unmarshal([]byte(standardResult), &decodedStandard))
	assert.NoError(t, json.Unmarshal([]byte(formatResult), &decodedFormat))

	// The decoded data should be identical
	assert.Equal(t, decodedStandard["html"], decodedFormat["html"])
	assert.Equal(t, decodedStandard["script"], decodedFormat["script"])
	assert.Equal(t, decodedStandard["comparison"], decodedFormat["comparison"])
}
