package gitlab

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestCanary_ClientDoesNotLeakCredential(t *testing.T) {
	logs := testutil.CaptureSlog(t)
	ctx := context.Background()
	var texts []string

	c := NewClient(testutil.CanaryToken, "group/proj")
	c.httpClient.Transport = testutil.FailingTransport()
	_, err := c.GetProject(ctx)
	texts = append(texts, testutil.ErrText(t, err))

	for _, status := range []int{401, 500} {
		srv := testutil.EchoServer(t, status)
		_, err := NewClientWithBaseURL(testutil.CanaryToken, "group/proj", srv.URL).GetProject(ctx)
		texts = append(texts, testutil.ErrText(t, err))
	}

	texts = append(texts, logs.String())
	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, texts...)
}
