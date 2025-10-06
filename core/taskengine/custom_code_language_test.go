package taskengine

import (
	"testing"

	avsproto "github.com/AvaProtocol/EigenLayer-AVS/protobuf"
)

func TestCustomCodeLanguageConversion(t *testing.T) {
	// Test what the backend sees when language enum is set
	config := &avsproto.CustomCodeNode_Config{
		Lang:   avsproto.Lang_LANG_JAVASCRIPT, // This is the enum value 0
		Source: "console.log('test');",
	}

	// Test what String() method returns
	langStr := config.Lang.String()
	t.Logf("Protobuf Lang.String() returns: '%s'", langStr)

	// This is what the JSProcessor expects to see
	if langStr != "JavaScript" {
		t.Errorf("Expected 'JavaScript', got '%s'", langStr)
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
		if actualLangStr != "JavaScript" {
			t.Errorf("JSProcessor should see 'JavaScript', but sees '%s'", actualLangStr)
		}
	}
}
