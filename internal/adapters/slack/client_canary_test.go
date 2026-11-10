package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qf-studio/pilot/internal/testutil"
)

func TestCanary_ClientDoesNotLeakCredential(t *testing.T) {
	logs := testutil.CaptureSlog(t)
	ctx := context.Background()
	var texts []string

	c := NewClient(testutil.CanaryToken)
	c.httpClient.Transport = testutil.FailingTransport()
	_, err := c.AuthTest(ctx)
	texts = append(texts, testutil.ErrText(t, err))

	// The server answers with a JSON body that carries the echoed request in
	// the field the client puts into its error, so the 401 and 500 legs reach
	// that path; a plain-text echo only fails JSON parsing.
	for _, status := range []int{401, 500} {
		srv := testutil.EchoServer(t, status, testutil.EchoJSONFields("error"))
		_, err := NewClientWithBaseURL(testutil.CanaryToken, srv.URL).AuthTest(ctx)
		texts = append(texts, testutil.ErrText(t, err))
	}

	texts = append(texts, logs.String())
	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, texts...)
}

// TestCanary_PostEphemeralDoesNotLeakEchoedBody: the response_url answer is
// copied into the error, so a body that echoes the token must be masked.
func TestCanary_PostEphemeralDoesNotLeakEchoedBody(t *testing.T) {
	logs := testutil.CaptureSlog(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("upstream failed, token " + testutil.CanaryToken))
	}))
	t.Cleanup(srv.Close)

	err := NewClient(testutil.CanaryToken).PostEphemeral(context.Background(), srv.URL, "hi")
	testutil.AssertNoCanaryLeak(t, testutil.CanaryToken, testutil.ErrText(t, err), logs.String())
}
