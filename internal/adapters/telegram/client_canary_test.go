package telegram

import (
	"context"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestCanary_ClientDoesNotLeakCredential(t *testing.T) {
	logs := testutil.CaptureSlog(t)
	ctx := context.Background()
	var texts []string

	c := NewClient(testutil.CanaryToken)
	c.httpClient.Transport = testutil.FailingTransport()
	_, err := c.GetMe(ctx)
	texts = append(texts, testutil.ErrText(t, err))

	// The server answers with a JSON body that carries the echoed request in
	// the field the client puts into its error, so the 401 and 500 legs reach
	// that path; a plain-text echo only fails JSON parsing.
	for _, status := range []int{401, 500} {
		srv := testutil.EchoServer(t, status, testutil.EchoJSONFields("description"))
		_, err := NewClientWithBaseURL(testutil.CanaryToken, srv.URL).GetMe(ctx)
		texts = append(texts, testutil.ErrText(t, err))
	}

	texts = append(texts, logs.String())
	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, texts...)
}
