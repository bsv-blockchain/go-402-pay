package pay402

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestHeaderPrefix(t *testing.T) {
	assert.Equal(t, "x-bsv-", HeaderPrefix)
}

func TestDefaultPaymentWindowMs(t *testing.T) {
	assert.Equal(t, 30_000, DefaultPaymentWindowMs)
}

func TestBRC29ProtocolID(t *testing.T) {
	assert.Equal(t, 2, int(BRC29ProtocolID.SecurityLevel))
	assert.Equal(t, "3241645161d8", BRC29ProtocolID.Protocol)
}

func TestAllHeadersHavePrefix(t *testing.T) {
	headers := []string{
		HeaderSats, HeaderServer,
		HeaderBeef, HeaderSender, HeaderNonce, HeaderTime, HeaderVout,
	}
	for _, h := range headers {
		assert.True(t, strings.HasPrefix(h, HeaderPrefix), "header %q should start with %q", h, HeaderPrefix)
	}
}

func TestServerToClientHeaders(t *testing.T) {
	assert.Equal(t, "x-bsv-sats", HeaderSats)
	assert.Equal(t, "x-bsv-server", HeaderServer)
}

func TestClientToServerHeaders(t *testing.T) {
	assert.Equal(t, "x-bsv-beef", HeaderBeef)
	assert.Equal(t, "x-bsv-sender", HeaderSender)
	assert.Equal(t, "x-bsv-nonce", HeaderNonce)
	assert.Equal(t, "x-bsv-time", HeaderTime)
	assert.Equal(t, "x-bsv-vout", HeaderVout)
}

func TestHeaderCount(t *testing.T) {
	all := []string{
		HeaderSats, HeaderServer,
		HeaderBeef, HeaderSender, HeaderNonce, HeaderTime, HeaderVout,
	}
	assert.Len(t, all, 7)
}
