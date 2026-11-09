package github

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestCanary_ClientDoesNotLeakCredential(t *testing.T) {
	logs := testutil.CaptureSlog(t)
	ctx := context.Background()
	var texts []string

	c := NewClientWithBaseURL(testutil.CanaryToken, "https://api.github.test")
	c.httpClient.Transport = testutil.FailingTransport()
	_, err := c.GetRepository(ctx, "o", "r")
	texts = append(texts, testutil.ErrText(t, err))
	_, err = c.GetJobLogs(ctx, "o", "r", 1)
	texts = append(texts, testutil.ErrText(t, err))

	for _, status := range []int{401, 500} {
		srv := testutil.EchoServer(t, status)
		c := NewClientWithBaseURL(testutil.CanaryToken, srv.URL)
		_, err := c.GetRepository(ctx, "o", "r")
		texts = append(texts, testutil.ErrText(t, err))
		_, err = c.GetJobLogs(ctx, "o", "r", 1)
		texts = append(texts, testutil.ErrText(t, err))
	}

	texts = append(texts, logs.String())
	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, texts...)
}
