package discord

import (
	"bytes"
	"context"
	"log/slog"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestCanary_ClientDoesNotLeakCredential(t *testing.T) {
	logs := testutil.CaptureSlog(t)
	ctx := context.Background()
	var clientLogs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&clientLogs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	var texts []string

	c := NewClient(testutil.CanaryToken)
	c.log = logger
	c.httpClient.Transport = testutil.FailingTransport()
	_, err := c.GetGatewayURL(ctx)
	texts = append(texts, testutil.ErrText(t, err))

	for _, status := range []int{401, 500} {
		srv := testutil.EchoServer(t, status)
		c := NewClientWithBaseURL(testutil.CanaryToken, srv.URL)
		c.log = logger
		_, err := c.GetGatewayURL(ctx)
		texts = append(texts, testutil.ErrText(t, err))
	}

	texts = append(texts, logs.String(), clientLogs.String())
	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, texts...)
}
