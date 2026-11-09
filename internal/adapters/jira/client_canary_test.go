package jira

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestCanary_ClientDoesNotLeakCredential(t *testing.T) {
	logs := testutil.CaptureSlog(t)
	ctx := context.Background()
	var texts []string

	c := NewClient("https://jira.test", "user@example.test", testutil.CanaryToken, PlatformCloud)
	c.httpClient.Transport = testutil.FailingTransport()
	_, err := c.GetIssue(ctx, "PROJ-1")
	texts = append(texts, testutil.ErrText(t, err))

	for _, status := range []int{401, 500} {
		srv := testutil.EchoServer(t, status)
		_, err := NewClient(srv.URL, "user@example.test", testutil.CanaryToken, PlatformCloud).GetIssue(ctx, "PROJ-1")
		texts = append(texts, testutil.ErrText(t, err))
	}

	texts = append(texts, logs.String())
	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, texts...)
	// The token travels base64-encoded in the Basic auth header.
	testutil.AssertNoCanaryLeak(t, base64.StdEncoding.EncodeToString([]byte("user@example.test:"+testutil.CanaryToken)), texts...)
}
