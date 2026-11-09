package azuredevops

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

	c := NewClient(testutil.CanaryToken, "org", "proj")
	c.httpClient.Transport = testutil.FailingTransport()
	_, err := c.GetWorkItem(ctx, 1)
	texts = append(texts, testutil.ErrText(t, err))

	for _, status := range []int{401, 500} {
		srv := testutil.EchoServer(t, status)
		_, err := NewClientWithBaseURL(testutil.CanaryToken, "org", "proj", srv.URL).GetWorkItem(ctx, 1)
		texts = append(texts, testutil.ErrText(t, err))
	}

	texts = append(texts, logs.String())
	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, texts...)
	// The PAT travels base64-encoded in the Basic auth header.
	testutil.AssertNoCanaryLeak(t, base64.StdEncoding.EncodeToString([]byte(":"+testutil.CanaryToken)), texts...)
}
