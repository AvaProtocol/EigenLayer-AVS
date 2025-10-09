package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestCustomCodeLanguageConversion(t *testing.T) {
	// Test what the backend sees when language enum is set
	config := &avsproto.CustomCodeNode_Config{
		Lang:   avsproto.Lang_LANG_JAVASCRIPT,
		Source: "console.log('test');",
	}

	// Test what String() method returns
	langStr := config.Lang.String()
	t.Logf("Protobuf Lang.String() returns: '%s'", langStr)

	// This is what the protobuf enum string representation is
	expectedStr := avsproto.Lang_LANG_JAVASCRIPT.String()
	if langStr != expectedStr {
		t.Errorf("Expected '%s', got '%s'", expectedStr, langStr)
	}

	// Test with CustomCodeNode to simulate real usage
	node := &avsproto.CustomCodeNode{
		Config: config,
	}

	// Simulate what JSProcessor.Execute() does
	if node.Config != nil {
		actualLangStr := node.Config.Lang.String()
		t.Logf("JSProcessor sees language as: '%s'", actualLangStr)

		// Verify it matches protobuf enum string representation
		if actualLangStr != expectedStr {
			t.Errorf("JSProcessor should see '%s', but sees '%s'", expectedStr, actualLangStr)
		}
	}
}
