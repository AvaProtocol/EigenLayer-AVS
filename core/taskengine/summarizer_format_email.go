package taskengine

import (
	"html"
	"strings"
	"time"
)

// SendGridDynamicData returns a dynamic_template_data map for SendGrid Dynamic Templates.
// The template uses these variables to render the email:
// - subject: email subject line
// - preheader: short preview text (reuses subject)
// - summary: one-line execution summary
// - status: execution status (success/partial_success/failure)
// - status_color: color for status badge (green/yellow/red)
// - trigger: what triggered the workflow
// - triggered_at: formatted timestamp
// - executions: array of on-chain operation descriptions
// - has_executions: boolean for conditional rendering
// - errors: array of error descriptions
// - has_errors: boolean for conditional rendering
// - analysisHtml: (legacy) pre-formatted HTML for backward compatibility
func (s Summary) SendGridDynamicData() map[string]interface{} {
	data := map[string]interface{}{
		"subject":   s.Subject,
		"preheader": s.Subject,
	}

	// New structured format (from context-memory API)
	if s.SummaryLine != "" {
		data["summary"] = s.SummaryLine
	}

	if s.Status != "" {
		data["status"] = s.Status
		data["status_color"] = getStatusColor(s.Status)
	}

	if s.Trigger != "" {
		data["trigger"] = s.Trigger
	}

	if s.TriggeredAt != "" {
		data["triggered_at"] = formatTimestampForEmail(s.TriggeredAt)
	}

	if len(s.Executions) > 0 {
		// Convert ExecutionEntry to maps for SendGrid template compatibility
		execMaps := make([]map[string]string, len(s.Executions))
		for i, e := range s.Executions {
			m := map[string]string{"description": e.Description}
			if e.TxHash != "" {
				m["txHash"] = e.TxHash
				m["txUrl"] = buildTxExplorerURL(s, e.TxHash)
			}
			execMaps[i] = m
		}
		data["executions"] = execMaps
		data["has_executions"] = true
	}

	if len(s.Errors) > 0 {
		data["errors"] = s.Errors
		data["has_errors"] = true
	}

	if s.Annotation != "" {
		data["annotation"] = s.Annotation
	}

	// Build analysisHtml from structured data
	var analysisHtml string
	if len(s.Executions) > 0 || len(s.Errors) > 0 || s.Trigger != "" {
		analysisHtml = buildAnalysisHtmlFromStructured(s)
	} else {
		// Fallback: build from plain text body
		clean := filterAnalysisTextForTemplate(s.Body)
		analysisHtml = buildBareHTMLFromText(clean)
	}
	data["analysisHtml"] = analysisHtml

	return data
}

// getStatusColor returns the color for email status badge based on execution status
func getStatusColor(status string) string {
	switch status {
	case "success":
		return "green"
	case "partial_success":
		return "yellow"
	case "failure":
		return "red"
	default:
		return "gray"
	}
}

// formatTimestampForEmail formats an ISO 8601 timestamp for email display
func formatTimestampForEmail(isoTimestamp string) string {
	t, err := time.Parse(time.RFC3339, isoTimestamp)
	if err != nil {
		// Try parsing without timezone
		t, err = time.Parse("2006-01-02T15:04:05", isoTimestamp)
		if err != nil {
			return isoTimestamp // Return as-is if parsing fails
		}
	}
	return t.Format("Jan 2, 2006 at 3:04 PM UTC")
}

// buildAnalysisHtmlFromStructured builds HTML content from the structured Summary fields
// This provides backward compatibility for older email templates that expect analysisHtml
// NOTE: Does NOT include summary line - use the separate {{summary}} template variable for that
// Uses consistent section headers with margin-bottom formatting
func buildAnalysisHtmlFromStructured(s Summary) string {
	var sb strings.Builder

	// Section 1: What Triggered This Workflow
	if s.Trigger != "" {
		sb.WriteString(`<div style="margin-bottom: 20px;">`)
		sb.WriteString(`<h3 style="margin: 0 0 8px 0; font-size: 16px;">What Triggered This Workflow</h3>`)
		sb.WriteString("<p style=\"margin: 0;\">✓ ")
		sb.WriteString(html.EscapeString(s.Trigger))
		sb.WriteString("</p>")
		// Add timestamp row if available
		if s.TriggeredAt != "" {
			if ts := formatTimestampForEmail(s.TriggeredAt); ts != "" {
				sb.WriteString("<p style=\"margin: 4px 0 0 18px; color: #666; font-size: 14px;\">")
				sb.WriteString(html.EscapeString(ts))
				sb.WriteString("</p>")
			}
		}
		sb.WriteString("</div>")
	}

	// Section 2: What Executed On-Chain
	if len(s.Executions) > 0 {
		sb.WriteString(`<div style="margin-bottom: 20px;">`)
		sb.WriteString(`<h3 style="margin: 0 0 8px 0; font-size: 16px;">What Executed On-Chain</h3>`)
		for _, exec := range s.Executions {
			sb.WriteString("<p style=\"margin: 0 0 4px 0;\">✓ ")
			sb.WriteString(html.EscapeString(exec.Description))
			sb.WriteString("</p>")
			if exec.TxHash != "" {
				if explorerURL := buildTxExplorerURL(s, exec.TxHash); explorerURL != "" {
					sb.WriteString("<p style=\"margin: 4px 0 0 18px; color: #666; font-size: 14px;\">Transaction: <a href=\"")
					sb.WriteString(html.EscapeString(explorerURL))
					sb.WriteString("\" style=\"color: #8B5CF6;\">")
					sb.WriteString(html.EscapeString(truncateTxHash(exec.TxHash)))
					sb.WriteString("</a></p>")
				}
			}
		}
		sb.WriteString("</div>")
	}

	// Section 3: What Went Wrong (only show if there are errors)
	if len(s.Errors) > 0 {
		sb.WriteString(`<div style="margin-bottom: 20px;">`)
		sb.WriteString(`<h3 style="margin: 0 0 8px 0; font-size: 16px;">What Went Wrong</h3>`)
		for _, err := range s.Errors {
			sb.WriteString("<p style=\"margin: 0 0 4px 0;\">✗ ")
			sb.WriteString(html.EscapeString(err))
			sb.WriteString("</p>")
		}
		sb.WriteString("</div>")
	}

	if s.Annotation != "" {
		sb.WriteString(`<div style="margin-top: 16px;">`)
		sb.WriteString("<p style=\"margin: 0; font-style: italic; color: #666;\">")
		sb.WriteString(html.EscapeString(s.Annotation))
		sb.WriteString("</p></div>")
	}

	return sb.String()
}

// buildBareHTMLFromText converts plain text into minimal HTML paragraphs without global styles
// Preserves safe HTML tags (<strong>, <em>, <br/>, etc.) while escaping potentially dangerous content
//
// SECURITY NOTE: This function uses a simple string replacement approach which has limitations.
// It does not validate HTML attributes, so tags like <strong onclick="..."> would pass through.
// For production use with untrusted input, consider using a proper HTML sanitizer library like
// github.com/microcosm-cc/bluemonday. This current implementation is acceptable for trusted
// AI-generated content from context-memory API, but should be reviewed if processing
// user-provided content.
func buildBareHTMLFromText(body string) string {
	// Normalize newlines first
	normalized := strings.ReplaceAll(body, "\r\n", "\n")

	// Check if body already contains HTML tags (from AI summaries)
	// If it does, preserve safe HTML tags and only escape unsafe content
	// Note: This check could be fooled by legitimate text containing comparison operators
	hasHTML := strings.Contains(normalized, "<") && strings.Contains(normalized, ">")

	if hasHTML {
		// Body already contains HTML - preserve safe tags and escape only unsafe content
		// First, temporarily replace safe HTML tags with placeholders
		safeTags := map[string]string{
			"<strong>":  "___STRONG_OPEN___",
			"</strong>": "___STRONG_CLOSE___",
			"<em>":      "___EM_OPEN___",
			"</em>":     "___EM_CLOSE___",
			"<br/>":     "___BR___",
			"<br>":      "___BR___",
		}

		// Replace safe tags with placeholders
		withPlaceholders := normalized
		for tag, placeholder := range safeTags {
			withPlaceholders = strings.ReplaceAll(withPlaceholders, tag, placeholder)
		}

		// Escape all remaining HTML (potentially dangerous)
		safe := html.EscapeString(withPlaceholders)

		// Restore safe tags
		for tag, placeholder := range safeTags {
			safe = strings.ReplaceAll(safe, placeholder, tag)
		}

		normalized = safe
	} else {
		// Plain text - escape everything
		normalized = html.EscapeString(normalized)
	}

	// Split by paragraphs (double newline)
	parts := strings.Split(normalized, "\n\n")
	var paragraphs []string
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		// Convert single newlines within a paragraph to <br/> (only if not already HTML)
		if !hasHTML {
			p = strings.ReplaceAll(p, "\n", "<br/>")
		}
		paragraphs = append(paragraphs, "<p>"+p+"</p>")
	}
	return strings.Join(paragraphs, "\n")
}

// filterAnalysisTextForTemplate removes lines that duplicate data provided via separate template variables
// such as runner/eoaAddress and high-level status lines. It preserves the core narrative/actions.
func filterAnalysisTextForTemplate(body string) string {
	if strings.TrimSpace(body) == "" {
		return body
	}
	b := strings.ReplaceAll(body, "\r\n", "\n")
	lines := strings.Split(b, "\n")
	var kept []string
	for _, ln := range lines {
		trim := strings.TrimSpace(ln)
		lower := strings.ToLower(trim)
		if trim == "" {
			// keep paragraph breaks; we'll collapse later
			kept = append(kept, trim)
			continue
		}
		// Drop runner/owner and generic completion/status lines
		if strings.HasPrefix(trim, "Smart wallet ") ||
			strings.HasPrefix(trim, "Runner smart wallet") ||
			strings.HasPrefix(trim, "All steps completed") ||
			strings.HasPrefix(trim, "Workflow '") ||
			strings.HasPrefix(trim, "The email was sent successfully via SendGrid") ||
			strings.HasPrefix(trim, "Single-node execution") ||
			strings.HasPrefix(lower, "smart wallet ") ||
			strings.HasPrefix(lower, "runner smart wallet") {
			continue
		}
		kept = append(kept, ln)
	}
	// Collapse extra blank lines: ensure at most one blank line between paragraphs
	var collapsed []string
	prevBlank := false
	for _, ln := range kept {
		if strings.TrimSpace(ln) == "" {
			if !prevBlank {
				collapsed = append(collapsed, "")
			}
			prevBlank = true
			continue
		}
		collapsed = append(collapsed, ln)
		prevBlank = false
	}
	res := strings.Join(collapsed, "\n")
	res = strings.TrimSpace(res)
	if res == "" {
		// Preserve original fallback message if present
		if strings.Contains(b, "No specific on-chain actions were recorded") {
			return "No specific on-chain actions were recorded; this may have been a simulation or a step encountered an error."
		}
	}
	return res
}

// buildStyledHTMLEmailForSummary wraps a plain-text body into a styled HTML layout
// suitable for email clients with light theme
func buildStyledHTMLEmailForSummary(subject, body string) string {
	// Escape HTML to avoid injection
	safe := html.EscapeString(body)
	// Normalize newlines
	safe = strings.ReplaceAll(safe, "\r\n", "\n")
	// Split by paragraphs (double newline)
	parts := strings.Split(safe, "\n\n")
	var paragraphs []string
	for _, p := range parts {
		if strings.TrimSpace(p) == "" {
			continue
		}
		// Convert single newlines within a paragraph to <br/>
		p = strings.ReplaceAll(p, "\n", "<br/>")
		paragraphs = append(paragraphs, "<p style=\"margin:0 0 16px 0;\">"+p+"</p>")
	}

	content := strings.Join(paragraphs, "\n")
	// Minimal, responsive-friendly light theme
	return "<!DOCTYPE html><html><head><meta charset=\"UTF-8\"><meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">" +
		"<title>" + html.EscapeString(subject) + "</title>" +
		"<style>body{background:#FFFFFF;color:#1F2937;margin:0;padding:0;font-family:Arial,Helvetica,sans-serif;-webkit-font-smoothing:antialiased;}" +
		".container{max-width:640px;margin:0 auto;padding:32px 24px;}h1,h2,h3,h4,h5,h6{color:#111827;margin-top:24px;margin-bottom:12px;}a{color:#8B5CF6;text-decoration:none;}" +
		"p{margin:0 0 16px 0;line-height:1.6;}.divider{border-top:1px solid #E5E7EB;margin:24px 0;}" +
		"@media(max-width:480px){.container{padding:24px 16px;}h1,h2,h3{font-size:1.2rem;}}</style></head>" +
		"<body><div class=\"container\">" + content + "</div></body></html>"
}
