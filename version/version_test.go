package version

import (
	"strings"
	"testing"
)

func TestSentryRelease(t *testing.T) {
	got := SentryRelease()
	want := Get() + "@" + GetRevision()
	if got != want {
		t.Fatalf("SentryRelease() = %q, want %q", got, want)
	}
	if !strings.Contains(got, "@") {
		t.Fatal("SentryRelease must be version@revision")
	}
}
