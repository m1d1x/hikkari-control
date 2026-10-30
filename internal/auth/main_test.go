package auth

import (
	"os"
	"testing"

	"github.com/m1d1x/hikkari-control/internal/cryptobox"
)

// TestMain loads a content key: this package records durable events, and every
// event is sealed on its way into the log (messaging.AppendEvent).
func TestMain(m *testing.M) {
	cryptobox.ConfigureEphemeralForTests()
	os.Exit(m.Run())
}
