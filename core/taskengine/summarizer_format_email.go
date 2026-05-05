package taskengine

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// buildSkippedNote returns the user-visible skip sentence rendered in the warn
// badge and as the `skipped_note` template variable. Kept in one place so the
// deterministic summarizer and the vm_runner_rest override can't drift.
func buildSkippedNote(count int) string {
	noun, verb := "node", "was"
	if count != 1 {
		noun, verb = "nodes", "were"
	}
	return fmt.Sprintf("%d %s %s skipped by Branch condition.", count, noun, verb)
}

// SendGridDynamicData returns a dynamic_template_data map for SendGrid Dynamic Templates.
// The template uses these variables to render the email:
// - subject: email subject line
// - preheader: short preview text (reuses subject)
// - summary: one-line execution summary (no skip-note suffix)
// - status: execution status ("success" | "failed" | "error")
// - status_color: color for status badge (green/yellow/red — yellow when success with skipped steps)
// - status_text: badge text ("All steps completed successfully" | SkippedNote | "Execution failed" | "System error")
// - skipped_note: "1 node was skipped by Branch condition." — present only when status=success && skippedSteps>0
// - skipped_steps / executed_steps / total_steps: step counts
// - trigger: what triggered the workflow
// - triggered_at: formatted timestamp
// - executions: array of on-chain operation descriptions
// - has_executions: boolean for conditional rendering (false when context-memory sends empty executions[])
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
		data["status_color"] = getStatusColor(s)
		data["status_text"] = getStatusDisplayText(s)
	}

	if s.SkippedNote != "" {
		data["skipped_note"] = s.SkippedNote
	}
	if s.TotalSteps > 0 {
		data["executed_steps"] = s.ExecutedSteps
		data["total_steps"] = s.TotalSteps
		data["skipped_steps"] = s.SkippedSteps
	}

	if s.Trigger != "" {
		data["trigger"] = s.Trigger
	}

	if s.TriggeredAt != "" {
		data["triggered_at"] = formatTimestampHumanReadable(s.TriggeredAt)
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

// getStatusColor returns the color for the email status badge.
// Per Studio, a `success` run with branch-skipped steps renders yellow ("warn"),
// not green — the skip is worth user attention even though nothing failed.
func getStatusColor(s Summary) string {
	switch s.Status {
	case "success":
		if s.SkippedSteps > 0 {
			return "yellow"
		}
		return "green"
	case "failed", "error":
		return "red"
	default:
		return "gray"
	}
}

// getStatusDisplayText returns the badge text for the email.
// For success-with-skipped, it surfaces the skip note (e.g. "1 node was skipped by
// Branch condition.") so the badge explains why it turned yellow.
func getStatusDisplayText(s Summary) string {
	switch s.Status {
	case "success":
		if s.SkippedSteps > 0 && s.SkippedNote != "" {
			return s.SkippedNote
		}
		return "All steps completed successfully"
	case "failed":
		return "Execution failed"
	case "error":
		return "System error"
	default:
		return "Completed"
	}
}

// ---------------------------------------------------------------------------
// Shared formatting helpers for all notification channels
// ---------------------------------------------------------------------------
//
// These helpers ensure consistent rendering across email, Telegram, Discord,
// and plain-text channels. When adding a new notification channel, follow
// the rules below for each field type.
//
// ## Context-memory API string conventions
//
// The context-memory API returns structured data that the aggregator formats
// per-channel. Two conventions apply to string fields:
//
//   - Backtick-delimited code: expressions or variable names are wrapped in
//     backticks (e.g., `code1.data.balance >= code1.data.totalNeeded`).
//     Use formatBackticksForChannel to render them appropriately.
//
//   - ISO 8601 timestamps: the triggeredAt field is in RFC 3339 format.
//     Use formatTimestampHumanReadable to convert to display format.
//
// ## Per-channel rendering rules
//
//   Field         | Telegram (HTML)         | Email (HTML)            | Discord (Markdown)  | Plaintext
//   --------------|-------------------------|-------------------------|---------------------|----------
//   Subject       | <code>name</code>       | template variable       | **bold**            | as-is
//   Network       | <b>Network:</b> value   | template variable       | plain text          | plain text
//   Time          | <b>Time:</b> formatted  | template variable       | —                   | —
//   Trigger       | <b>Trigger:</b> value   | template variable       | plain text          | plain text
//   Executions    | <b>Executed:</b> • list  | template variable       | • list              | - list
//   Errors        | <b>What Went Wrong:</b> | "What Went Wrong" <h3>  | **What Went Wrong:**| What Went Wrong:
//   Backticks     | → <code>...</code>      | → <code>...</code>      | native (pass-thru)  | pass-thru
//   Timestamps    | formatTimestampHumanReadable                                             | (same for all)
//   Annotation    | <i>text</i>             | italic styled           | *text*              | plain text
// ---------------------------------------------------------------------------

// formatTimestampHumanReadable formats an ISO 8601 timestamp into a human-readable string.
// Used by all notification channels (email, telegram, discord) for consistent display.
func formatTimestampHumanReadable(isoTimestamp string) string {
	t, err := time.Parse(time.RFC3339, isoTimestamp)
	if err != nil {
		// Try parsing without timezone
		t, err = time.Parse("2006-01-02T15:04:05", isoTimestamp)
		if err != nil {
			return isoTimestamp // Return as-is if parsing fails
		}
	}
	return t.UTC().Format("Jan 2, 2006 at 3:04 PM UTC")
}

// formatBackticksToHTML converts backtick-delimited segments in a string to <code> tags.
// Used by Telegram and email formatters to render inline code from context-memory API errors.
// Non-backticked portions are HTML-escaped; backticked portions are wrapped in <code> tags.
//
// Context-memory API convention:
//
//	The context-memory API uses backticks to delimit code expressions in error messages.
//	Example input:  "loop1 - condition not met: `balance >= totalNeeded` evaluated to false"
//	Example output: "loop1 - condition not met: <code>balance &gt;= totalNeeded</code> evaluated to false"
//
// See formatBackticksForChannel for the channel-routing wrapper.
func formatBackticksToHTML(s string) string {
	var sb strings.Builder
	for {
		start := strings.Index(s, "`")
		if start < 0 {
			sb.WriteString(html.EscapeString(s))
			break
		}
		end := strings.Index(s[start+1:], "`")
		if end < 0 {
			// No closing backtick — escape the rest as-is
			sb.WriteString(html.EscapeString(s))
			break
		}
		end += start + 1 // absolute index of closing backtick

		sb.WriteString(html.EscapeString(s[:start]))
		sb.WriteString("<code>")
		sb.WriteString(html.EscapeString(s[start+1 : end]))
		sb.WriteString("</code>")
		s = s[end+1:]
	}
	return sb.String()
}

// formatBackticksForChannel converts backtick-delimited code segments for the target channel.
//
// Channel formatting rules for context-memory API strings:
//
//   - Telegram: backticks → <code>...</code> (Telegram HTML parse mode)
//   - Email:    backticks → <code>...</code> (HTML email)
//   - Discord:  pass through as-is (Discord natively renders backticks as inline code)
//   - Plaintext: pass through as-is (backticks are readable in plain text)
//
// When adding a new notification channel, decide whether it supports inline code
// markup and add a case here. Default is pass-through.
func formatBackticksForChannel(s string, channel string) string {
	switch channel {
	case "telegram", "email":
		return formatBackticksToHTML(s)
	default:
		// Discord, plaintext, and future channels that handle backticks natively
		return s
	}
}

// buildAnalysisHtmlFromStructured builds HTML content from the structured Summary fields
// This provides backward compatibility for older email templates that expect analysisHtml
// NOTE: Does NOT include summary line - use the separate {{summary}} template variable for that
// Uses consistent section headers with margin-bottom formatting
func buildAnalysisHtmlFromStructured(s Summary) string {
	var sb strings.Builder

	// Section: What Triggered This Workflow
	if s.Trigger != "" {
		sb.WriteString(`<div style="margin-bottom: 20px;">`)
		sb.WriteString(`<h3 style="margin: 0 0 8px 0; font-size: 16px;">What Triggered This Workflow</h3>`)
		sb.WriteString("<p style=\"margin: 0;\">✓ ")
		sb.WriteString(html.EscapeString(s.Trigger))
		sb.WriteString("</p>")
		// Add timestamp row if available
		if s.TriggeredAt != "" {
			if ts := formatTimestampHumanReadable(s.TriggeredAt); ts != "" {
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
			sb.WriteString(formatBackticksForChannel(exec.Description, "email"))
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
			sb.WriteString(formatBackticksForChannel(err, "email"))
			sb.WriteString("</p>")
		}
		sb.WriteString("</div>")
	}

	// Section: Cost / Estimated cost — fee breakdown
	if s.Fees != nil {
		if costHTML := buildFeesSectionHTML(s); costHTML != "" {
			sb.WriteString(costHTML)
		}
	}

	if s.Annotation != "" {
		sb.WriteString(`<div style="margin-top: 16px;">`)
		sb.WriteString("<p style=\"margin: 0; font-style: italic; color: #666;\">")
		sb.WriteString(html.EscapeString(s.Annotation))
		sb.WriteString("</p></div>")
	}

	return sb.String()
}

// buildFeesSectionHTML renders the Cost section from Summary.Fees.Total —
// same multi-token format as Telegram, just wrapped in HTML. Simulations
// render only the static placeholder. Returns "" when there's nothing to show.
func buildFeesSectionHTML(s Summary) string {
	if s.Fees == nil {
		return ""
	}

	if s.Workflow != nil && s.Workflow.IsSimulation {
		// Heading omitted — the placeholder line carries enough context on its own.
		return `<div style="margin-bottom: 20px;">` +
			`<p style="margin: 0; color: #666; font-style: italic;">⛽ (see cost estimate before deploy)</p>` +
			`</div>`
	}

	if len(s.Fees.Total) == 0 {
		return ""
	}
	parts := make([]string, 0, len(s.Fees.Total))
	for _, t := range s.Fees.Total {
		if t == nil || t.Amount == "" || t.Amount == "0" {
			continue
		}
		// USD-unit entries are the platform fee — render as "$X platform fee".
		if t.Unit == "USD" {
			parts = append(parts, fmt.Sprintf("$%s platform fee", html.EscapeString(t.Amount)))
			continue
		}
		usd := "$?"
		if t.USD != "" {
			usd = "$" + t.USD
		}
		parts = append(parts, fmt.Sprintf("%s %s (%s)", html.EscapeString(t.Amount), html.EscapeString(t.Unit), usd))
	}
	if len(parts) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(`<div style="margin-bottom: 20px;">`)
	sb.WriteString(`<h3 style="margin: 0 0 8px 0; font-size: 16px;">Cost</h3>`)
	sb.WriteString(`<p style="margin: 0;">⛽ `)
	sb.WriteString(strings.Join(parts, ", "))
	sb.WriteString("</p></div>")
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
