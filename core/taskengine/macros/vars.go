package macros

import (
	"fmt"
	"strings"
)

func Render(text []byte, vars map[string]string) string {
	return RenderString(string(text), vars)
}

// TODO: Add more variable and documents these macros
func RenderString(text string, vars map[string]string) string {
	for k, v := range vars {
		text = strings.ReplaceAll(text, fmt.Sprintf("{{%s}}", k), v)
	}

	return text
}

// TODO: document all of our available secrets
// There is a certain operation we let use use it, but don't let user see it. Example to setup email or notifiction, behind the scene, they require an API key. So they can use their API key to send notification and craft the message the way they want, but they cannot see it.
func RenderSecrets(text string, vars map[string]string) string {
	for k, v := range vars {
		text = strings.ReplaceAll(text, fmt.Sprintf("${{secrets.%s}}", k), v)
	}

	return text
}
