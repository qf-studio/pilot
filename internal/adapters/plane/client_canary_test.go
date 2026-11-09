package plane

import (
	"context"
	"net/http"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestCanary_ClientDoesNotLeakCredential(t *testing.T) {
	logs := testutil.CaptureSlog(t)
	ctx := context.Background()
	var texts []string

	c := NewClient("https://plane.test", testutil.CanaryToken,
		WithHTTPClient(&http.Client{Transport: testutil.FailingTransport()}))
	_, err := c.GetWorkItem(ctx, "ws", "proj", "item-1")
	texts = append(texts, testutil.ErrText(t, err))

	for _, status := range []int{401, 500} {
		srv := testutil.EchoServer(t, status)
		_, err := NewClient(srv.URL, testutil.CanaryToken).GetWorkItem(ctx, "ws", "proj", "item-1")
		texts = append(texts, testutil.ErrText(t, err))
	}

	texts = append(texts, logs.String())
	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, texts...)
}
