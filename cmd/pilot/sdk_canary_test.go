package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	sdkTelegram "github.com/qf-studio/studio-sdk/sdk/integrations/telegram"

	"github.com/qf-studio/pilot/internal/testutil"
)

// sdkTelegramCanaryToken is the bot token of the SDK Telegram client under
// test. It is an obviously fake value; the test fails when it shows up in any
// text a caller can reach.
const sdkTelegramCanaryToken = "test-telegram-canary-bot-token"

// TestCanary_PinnedSDKTelegramClientDoesNotLeakToken proves, through the SDK
// version that go.mod pins, that the SDK Telegram client masks its bot token
// in a transport error. The token is part of the request URL, so before
// studio-sdk v0.39.3 each transport error carried it (GH-6727, TASK-659).
func TestCanary_PinnedSDKTelegramClientDoesNotLeakToken(t *testing.T) {
	logs := testutil.CaptureSlog(t)

	client := sdkTelegram.NewClient(sdkTelegramCanaryToken,
		sdkTelegram.WithHTTPClient(&http.Client{Transport: testutil.FailingTransport()}))

	_, err := client.SendMessage(context.Background(), "12345", "hello", "")
	errText := testutil.ErrText(t, err)

	texts := []string{
		errText,
		fmt.Sprintf("%v", err),
		fmt.Sprintf("%+v", err),
		fmt.Sprintf("%#v", err),
	}
	for e := errors.Unwrap(err); e != nil; e = errors.Unwrap(e) {
		texts = append(texts, e.Error())
	}

	var urlErr *url.Error
	if !errors.As(err, &urlErr) {
		t.Fatalf("errors.As(err, *url.Error) = false, want true; err: %s", errText)
	}
	texts = append(texts, urlErr.Error(), urlErr.URL)
	texts = append(texts, logs.String())

	testutil.AssertNoCanaryLeak(t, sdkTelegramCanaryToken, texts...)

	if !strings.Contains(errText, "[REDACTED]") {
		t.Errorf("error text does not hold [REDACTED], so the mask did not run: %s", errText)
	}
}
