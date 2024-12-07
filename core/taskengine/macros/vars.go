package macros

import (
	"fmt"
	"strings"
)

func Render(text []byte, vars map[string]string) string {
	return RenderString(string(text), vars)
}

// TODO: Add more variable and coument these macros
func RenderString(text string, vars map[string]string) string {
	for k, v := range vars {
		text = strings.ReplaceAll(text, fmt.Sprintf("{{%s}}", k), v)
	}

	return text
}
