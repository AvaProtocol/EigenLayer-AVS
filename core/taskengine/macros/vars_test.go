package macros

import (
	"testing"
)

func TestRenderSecret(t *testing.T) {
	text := RenderSecrets("this has ${{secrets.foo_token}}", map[string]string{
		"foo_token": "123abc",
	})

	if text != "this has 123abc" {
		t.Errorf("render secret doesn't render final text that contains the secrets. expect `this has 123abc` but got %s", text)
	}
}
