package slack

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

// TestCanary_SocketModeDoesNotLeakAppToken covers the app-level token that
// apps.connections.open sends; the bot-token test does not touch it.
func TestCanary_SocketModeDoesNotLeakAppToken(t *testing.T) {
	logs := testutil.CaptureSlog(t)
	ctx := context.Background()
	var texts []string

	c := NewSocketModeClient(testutil.CanaryToken)
	c.httpClient.Transport = testutil.FailingTransport()
	_, err := c.OpenConnection(ctx)
	texts = append(texts, testutil.ErrText(t, err))

	// Plain-text echo (HTTP status path) and JSON echo (the "error" field the
	// client puts into its error).
	for _, status := range []int{401, 500} {
		for _, opts := range [][]testutil.EchoOption{nil, {testutil.EchoJSONFields("error")}} {
			srv := testutil.EchoServer(t, status, opts...)
			_, err := NewSocketModeClientWithBaseURL(testutil.CanaryToken, srv.URL).OpenConnection(ctx)
			texts = append(texts, testutil.ErrText(t, err))
		}
	}

	texts = append(texts, logs.String())
	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, texts...)
}
